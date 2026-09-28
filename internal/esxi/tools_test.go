package esxi

import "testing"

// The shape of vim-cmd vmsvc/get.guest as documented for the vSphere GuestInfo
// object; not yet captured from a real host. Only toolsRunningStatus is read,
// and anything unexpected is an error rather than a yes or a no.
func TestParseToolsRunning(t *testing.T) {
	guest := func(status string) string {
		return "Guest information:\n\n(vim.vm.GuestInfo) {\n   toolsStatus = \"toolsOk\",\n   toolsVersionStatus = \"guestToolsCurrent\",\n   toolsRunningStatus = \"" + status + "\",\n   guestState = \"running\",\n}\n"
	}
	for status, want := range map[string]bool{"guestToolsRunning": true, "guestToolsExecutingScripts": true, "guestToolsNotRunning": false} {
		got, e := ParseToolsRunning(guest(status))
		if e != nil || got != want {
			t.Fatalf("%s: got %t, %v", status, got, e)
		}
	}
	for name, bad := range map[string]string{
		"unknown value": guest("somethingNew"),
		"missing":       "Guest information:\n\n(vim.vm.GuestInfo) {\n}\n",
		"twice":         guest("guestToolsRunning") + guest("guestToolsNotRunning"),
		"empty":         "",
	} {
		if _, e := ParseToolsRunning(bad); e == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
