package node

import "testing"

// Captured from `adb shell dumpsys account` on the fleet. The listing at the
// head is followed by history, authenticator, and visibility sections that name
// accounts again in other shapes.
const dumpsysAccountOutput = `User UserInfo{0:Ten:4c13}:
  Accounts: 3
    Account {name=thetenseneto2@gmail.com, type=com.google}
    Account {name=brijie.brown@gmail.com, type=com.google}
    Account {name=Meet, type=com.google.android.apps.tachyon}

  AccountId, Action_Type, timestamp, UID, TableName, Key
  Accounts History
  1,action_account_add,2026-02-11 08:33:39,10177,accounts,1
  -1,action_called_account_add,2026-04-03 21:47:01,10177,accounts,4

  Active Sessions: 0

  RegisteredServicesCache: 5 services
    ServiceInfo: AuthenticatorDescription {type=com.google}, ComponentInfo{com.google.android.gms/...}, uid 10177

  Account visibility:
    brijie.brown@gmail.com
      com.android.vending, 2
User UserInfo{10: Island :1030}:
  Accounts: 1
    Account {name=thepopman6528@gmail.com, type=com.google}
`

func TestParseDeviceAccountsKeepsListingOrderPerUser(t *testing.T) {
	accounts := parseDeviceAccounts(dumpsysAccountOutput)

	want := []DeviceAccount{
		{Email: "thetenseneto2@gmail.com", UserID: 0, UserName: "Ten"},
		{Email: "brijie.brown@gmail.com", UserID: 0, UserName: "Ten"},
		{Email: "thepopman6528@gmail.com", UserID: 10, UserName: "Island"},
	}
	if len(accounts) != len(want) {
		t.Fatalf("got %d accounts, want %d: %+v", len(accounts), len(want), accounts)
	}
	for i, account := range accounts {
		if account != want[i] {
			t.Fatalf("account %d = %+v, want %+v", i, account, want[i])
		}
	}
}

func TestParseDeviceAccountsRejectsPrefixSharingTypes(t *testing.T) {
	// com.google.android.apps.tachyon shares the com.google prefix but is not a
	// Google account, so a prefix match would report a phantom "Meet" account.
	for _, account := range parseDeviceAccounts(dumpsysAccountOutput) {
		if account.Email == "Meet" {
			t.Fatalf("tachyon account was reported as a Google account")
		}
	}
}

func TestParseDeviceAccountsIgnoresNonListingSections(t *testing.T) {
	// The visibility section names brijie.brown a second time; the account must
	// appear exactly once.
	seen := 0
	for _, account := range parseDeviceAccounts(dumpsysAccountOutput) {
		if account.Email == "brijie.brown@gmail.com" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("brijie.brown@gmail.com appeared %d times, want 1", seen)
	}
}

func TestParseDeviceAccountsHandlesEmptyDump(t *testing.T) {
	if accounts := parseDeviceAccounts(""); len(accounts) != 0 {
		t.Fatalf("got %+v, want no accounts", accounts)
	}
}
