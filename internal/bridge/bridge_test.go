// Copyright (c) 2020 Proton Technologies AG
//
// This file is part of ProtonMail Bridge.
//
// ProtonMail Bridge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// ProtonMail Bridge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with ProtonMail Bridge.  If not, see <https://www.gnu.org/licenses/>.

package bridge

import (
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/ProtonMail/proton-bridge/internal/bridge/credentials"
	bridgemocks "github.com/ProtonMail/proton-bridge/internal/bridge/mocks"
	"github.com/ProtonMail/proton-bridge/internal/events"
	"github.com/ProtonMail/proton-bridge/internal/metrics"
	"github.com/ProtonMail/proton-bridge/internal/preferences"
	"github.com/ProtonMail/proton-bridge/internal/store"
	"github.com/ProtonMail/proton-bridge/pkg/pmapi"
	gomock "github.com/golang/mock/gomock"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if os.Getenv("VERBOSITY") == "trace" {
		logrus.SetLevel(logrus.TraceLevel)
	}
	os.Exit(m.Run())
}

var (
	testAuth = &pmapi.Auth{ //nolint[gochecknoglobals]
		RefreshToken: "tok",
		KeySalt:      "", // No salting in tests.
	}
	testAuthRefresh = &pmapi.Auth{ //nolint[gochecknoglobals]
		RefreshToken: "reftok",
		KeySalt:      "", // No salting in tests.
	}

	testCredentials = &credentials.Credentials{ //nolint[gochecknoglobals]
		UserID:                "user",
		Name:                  "username",
		Emails:                "user@pm.me",
		APIToken:              "token",
		MailboxPassword:       "pass",
		BridgePassword:        "0123456789abcdef",
		Version:               "v1",
		Timestamp:             123456789,
		IsHidden:              false,
		IsCombinedAddressMode: true,
	}
	testCredentialsSplit = &credentials.Credentials{ //nolint[gochecknoglobals]
		UserID:                "users",
		Name:                  "usersname",
		Emails:                "users@pm.me;anotheruser@pm.me;alsouser@pm.me",
		APIToken:              "token",
		MailboxPassword:       "pass",
		BridgePassword:        "0123456789abcdef",
		Version:               "v1",
		Timestamp:             123456789,
		IsHidden:              false,
		IsCombinedAddressMode: false,
	}
	testCredentialsDisconnected = &credentials.Credentials{ //nolint[gochecknoglobals]
		UserID:                "user",
		Name:                  "username",
		Emails:                "user@pm.me",
		APIToken:              "",
		MailboxPassword:       "",
		BridgePassword:        "0123456789abcdef",
		Version:               "v1",
		Timestamp:             123456789,
		IsHidden:              false,
		IsCombinedAddressMode: true,
	}

	testPMAPIUser = &pmapi.User{ //nolint[gochecknoglobals]
		ID:   "user",
		Name: "username",
	}

	testPMAPIAddress = &pmapi.Address{ //nolint[gochecknoglobals]
		ID:      "testAddressID",
		Type:    pmapi.OriginalAddress,
		Email:   "user@pm.me",
		Receive: pmapi.CanReceive,
	}

	testPMAPIAddresses = []*pmapi.Address{ //nolint[gochecknoglobals]
		{ID: "usersAddress1ID", Email: "users@pm.me", Receive: pmapi.CanReceive, Type: pmapi.OriginalAddress},
		{ID: "usersAddress2ID", Email: "anotheruser@pm.me", Receive: pmapi.CanReceive, Type: pmapi.AliasAddress},
		{ID: "usersAddress3ID", Email: "alsouser@pm.me", Receive: pmapi.CanReceive, Type: pmapi.AliasAddress},
	}

	testPMAPIEvent = &pmapi.Event{ // nolint[gochecknoglobals]
		EventID: "ACXDmTaBub14w==",
	}
)

func waitForEvents() {
	// Wait for goroutine to add listener.
	// E.g. calling login to invoke firstsync event. Functions can end sooner than
	// goroutines call the listener mock. We need to wait a little bit before the end of
	// the test to capture all event calls. This allows us to detect whether there were
	// missing calls, or perhaps whether something was called too many times.
	time.Sleep(100 * time.Millisecond)
}

type mocks struct {
	t *testing.T

	ctrl             *gomock.Controller
	config           *bridgemocks.MockConfiger
	PanicHandler     *bridgemocks.MockPanicHandler
	prefProvider     *bridgemocks.MockPreferenceProvider
	pmapiClient      *bridgemocks.MockPMAPIProvider
	credentialsStore *bridgemocks.MockCredentialsStorer
	eventListener    *MockListener

	storeCache *store.Cache
}

func initMocks(t *testing.T) mocks {
	mockCtrl := gomock.NewController(t)

	cacheFile, err := ioutil.TempFile("", "bridge-store-cache-*.db")
	require.NoError(t, err, "could not get temporary file for store cache")

	m := mocks{
		t: t,

		ctrl:             mockCtrl,
		config:           bridgemocks.NewMockConfiger(mockCtrl),
		PanicHandler:     bridgemocks.NewMockPanicHandler(mockCtrl),
		pmapiClient:      bridgemocks.NewMockPMAPIProvider(mockCtrl),
		prefProvider:     bridgemocks.NewMockPreferenceProvider(mockCtrl),
		credentialsStore: bridgemocks.NewMockCredentialsStorer(mockCtrl),
		eventListener:    NewMockListener(mockCtrl),

		storeCache: store.NewCache(cacheFile.Name()),
	}

	// Ignore heartbeat calls because they always happen.
	m.pmapiClient.EXPECT().SendSimpleMetric(string(metrics.Heartbeat), gomock.Any(), gomock.Any()).AnyTimes()
	m.prefProvider.EXPECT().Get(preferences.NextHeartbeatKey).AnyTimes()
	m.prefProvider.EXPECT().Set(preferences.NextHeartbeatKey, gomock.Any()).AnyTimes()

	// Called during clean-up.
	m.PanicHandler.EXPECT().HandlePanic().AnyTimes()

	return m
}

func testNewBridgeWithUsers(t *testing.T, m mocks) *Bridge {
	// Init for user.
	m.pmapiClient.EXPECT().AuthRefresh("token").Return(testAuthRefresh, nil)
	m.pmapiClient.EXPECT().Unlock("pass").Return(nil, nil)
	m.pmapiClient.EXPECT().UnlockAddresses([]byte("pass")).Return(nil)
	m.pmapiClient.EXPECT().ListLabels().Return([]*pmapi.Label{}, nil)
	m.pmapiClient.EXPECT().Addresses().Return([]*pmapi.Address{testPMAPIAddress})
	m.pmapiClient.EXPECT().CountMessages("").Return([]*pmapi.MessagesCount{}, nil)
	m.credentialsStore.EXPECT().Get("user").Return(testCredentials, nil).Times(2)
	m.credentialsStore.EXPECT().UpdateToken("user", ":reftok").Return(nil)
	m.credentialsStore.EXPECT().Get("user").Return(testCredentials, nil)
	m.pmapiClient.EXPECT().GetEvent("").Return(testPMAPIEvent, nil)
	m.pmapiClient.EXPECT().ListMessages(gomock.Any()).Return([]*pmapi.Message{}, 0, nil)
	m.pmapiClient.EXPECT().GetEvent(testPMAPIEvent.EventID).Return(testPMAPIEvent, nil)

	// Init for users.
	m.pmapiClient.EXPECT().AuthRefresh("token").Return(testAuthRefresh, nil)
	m.pmapiClient.EXPECT().Unlock("pass").Return(nil, nil)
	m.pmapiClient.EXPECT().UnlockAddresses([]byte("pass")).Return(nil)
	m.pmapiClient.EXPECT().ListLabels().Return([]*pmapi.Label{}, nil)
	m.pmapiClient.EXPECT().Addresses().Return(testPMAPIAddresses)
	m.pmapiClient.EXPECT().CountMessages("").Return([]*pmapi.MessagesCount{}, nil)
	m.credentialsStore.EXPECT().Get("users").Return(testCredentialsSplit, nil).Times(2)
	m.credentialsStore.EXPECT().UpdateToken("users", ":reftok").Return(nil)
	m.credentialsStore.EXPECT().Get("users").Return(testCredentialsSplit, nil)
	m.pmapiClient.EXPECT().GetEvent("").Return(testPMAPIEvent, nil)
	m.pmapiClient.EXPECT().ListMessages(gomock.Any()).Return([]*pmapi.Message{}, 0, nil)
	m.pmapiClient.EXPECT().GetEvent(testPMAPIEvent.EventID).Return(testPMAPIEvent, nil)

	m.credentialsStore.EXPECT().List().Return([]string{"user", "users"}, nil)

	return testNewBridge(t, m)
}

func testNewBridge(t *testing.T, m mocks) *Bridge {
	cacheFile, err := ioutil.TempFile("", "bridge-store-cache-*.db")
	require.NoError(t, err, "could not get temporary file for store cache")

	m.prefProvider.EXPECT().GetBool(preferences.FirstStartKey).Return(false).AnyTimes()
	m.prefProvider.EXPECT().GetBool(preferences.AllowProxyKey).Return(false).AnyTimes()
	m.config.EXPECT().GetDBDir().Return("/tmp").AnyTimes()
	m.config.EXPECT().GetIMAPCachePath().Return(cacheFile.Name()).AnyTimes()
	m.pmapiClient.EXPECT().SetAuths(gomock.Any()).AnyTimes()
	m.eventListener.EXPECT().Add(events.UpgradeApplicationEvent, gomock.Any())
	pmapiClientFactory := func(userID string) PMAPIProvider {
		log.WithField("userID", userID).Info("Creating new pmclient")
		return m.pmapiClient
	}

	bridge := New(m.config, m.prefProvider, m.PanicHandler, m.eventListener, "ver", pmapiClientFactory, m.credentialsStore)

	waitForEvents()

	return bridge
}

func cleanUpBridgeUserData(b *Bridge) {
	for _, user := range b.users {
		_ = user.clearStore()
	}
}

func TestClearData(t *testing.T) {
	m := initMocks(t)
	defer m.ctrl.Finish()

	bridge := testNewBridgeWithUsers(t, m)
	defer cleanUpBridgeUserData(bridge)

	m.eventListener.EXPECT().Emit(events.CloseConnectionEvent, "user@pm.me")
	m.eventListener.EXPECT().Emit(events.CloseConnectionEvent, "users@pm.me")
	m.eventListener.EXPECT().Emit(events.CloseConnectionEvent, "anotheruser@pm.me")
	m.eventListener.EXPECT().Emit(events.CloseConnectionEvent, "alsouser@pm.me")

	m.pmapiClient.EXPECT().Logout()
	m.credentialsStore.EXPECT().Logout("user").Return(nil)
	m.credentialsStore.EXPECT().Get("user").Return(testCredentials, nil)

	m.pmapiClient.EXPECT().Logout()
	m.credentialsStore.EXPECT().Logout("users").Return(nil)
	m.credentialsStore.EXPECT().Get("users").Return(testCredentialsSplit, nil)

	m.config.EXPECT().ClearData().Return(nil)

	require.NoError(t, bridge.ClearData())

	waitForEvents()
}
