// Copyright 2018 Project Harbor Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/gob"
	"fmt"
	"os"
	"strconv"

	"github.com/astaxie/beego"
	_ "github.com/astaxie/beego/session/redis"

	"github.com/goharbor/harbor/src/common/config/client/db"
	"github.com/goharbor/harbor/src/common/dao"
	"github.com/goharbor/harbor/src/common/models"
	"github.com/goharbor/harbor/src/common/utils"
	"github.com/goharbor/harbor/src/common/utils/log"
	"github.com/goharbor/harbor/src/core/api"
	_ "github.com/goharbor/harbor/src/core/auth/db"
	_ "github.com/goharbor/harbor/src/core/auth/ldap"
	_ "github.com/goharbor/harbor/src/core/auth/uaa"
	"github.com/goharbor/harbor/src/core/config"
	"github.com/goharbor/harbor/src/core/filter"
	"github.com/goharbor/harbor/src/core/notifier"
	"github.com/goharbor/harbor/src/core/proxy"
	"github.com/goharbor/harbor/src/core/service/token"
	"github.com/goharbor/harbor/src/replication/core"
	_ "github.com/goharbor/harbor/src/replication/event"
)

const (
	adminUserID = 1
)

func updateInitPassword(userID int, password string) error {
	queryUser := models.User{UserID: userID}
	user, err := dao.GetUser(queryUser)
	if err != nil {
		return fmt.Errorf("Failed to get user, userID: %d %v", userID, err)
	}
	if user == nil {
		return fmt.Errorf("user id: %d does not exist", userID)
	}
	if user.Salt == "" {
		salt := utils.GenerateRandomString()

		user.Salt = salt
		user.Password = password
		err = dao.ChangeUserPassword(*user)
		if err != nil {
			return fmt.Errorf("Failed to update user encrypted password, userID: %d, err: %v", userID, err)
		}

		log.Infof("User id: %d updated its encypted password successfully.", userID)
	} else {
		log.Infof("User id: %d already has its encrypted password.", userID)
	}
	return nil
}

func main() {
	beego.BConfig.WebConfig.Session.SessionOn = true
	// TODO
	redisURL := os.Getenv("_REDIS_URL")
	if len(redisURL) > 0 {
		gob.Register(models.User{})
		beego.BConfig.WebConfig.Session.SessionProvider = "redis"
		beego.BConfig.WebConfig.Session.SessionProviderConfig = redisURL
	}
	beego.AddTemplateExt("htm")

	log.Info("initializing configurations...")
	if err := config.Init(); err != nil {
		log.Fatalf("failed to initialize configurations: %v", err)
	}
	log.Info("configurations initialization completed")
	token.InitCreators()

	db.InitDatabaseAndConfigure()

	password, err := config.InitialAdminPassword()
	if err != nil {
		log.Fatalf("failed to get admin's initia password: %v", err)
	}
	if err := updateInitPassword(adminUserID, password); err != nil {
		log.Error(err)
	}

	// Init API handler
	if err := api.Init(); err != nil {
		log.Fatalf("Failed to initialize API handlers with error: %s", err.Error())
	}

	// Subscribe the policy change topic.
	if err = notifier.Subscribe(notifier.ScanAllPolicyTopic, &notifier.ScanPolicyNotificationHandler{}); err != nil {
		log.Errorf("failed to subscribe scan all policy change topic: %v", err)
	}

	if config.WithClair() {
		clairDB, err := config.ClairDB()
		if err != nil {
			log.Fatalf("failed to load clair database information: %v", err)
		}
		if err := dao.InitClairDB(clairDB); err != nil {
			log.Fatalf("failed to initialize clair database: %v", err)
		}
	}

	if err := core.Init(); err != nil {
		log.Errorf("failed to initialize the replication controller: %v", err)
	}

	filter.Init()
	beego.InsertFilter("/*", beego.BeforeRouter, filter.SecurityFilter)
	beego.InsertFilter("/*", beego.BeforeRouter, filter.ReadonlyFilter)
	beego.InsertFilter("/api/*", beego.BeforeRouter, filter.MediaTypeFilter("application/json", "multipart/form-data", "application/octet-stream"))

	initRouters()

	syncRegistry := os.Getenv("SYNC_REGISTRY")
	sync, err := strconv.ParseBool(syncRegistry)
	if err != nil {
		log.Errorf("Failed to parse SYNC_REGISTRY: %v", err)
		// if err set it default to false
		sync = false
	}
	if sync {
		if err := api.SyncRegistry(config.GlobalProjectMgr); err != nil {
			log.Error(err)
		}
	} else {
		log.Infof("Because SYNC_REGISTRY set false , no need to sync registry \n")
	}

	log.Info("Init proxy")
	proxy.Init()
	// go proxy.StartProxy()
	beego.Run()
}