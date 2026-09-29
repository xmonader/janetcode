package shell

import "strings"

// seatbeltMachServices are the Mach services a sandboxed command may
// look up. "(allow mach-lookup)" with no filter let a command reach
// every service on the machine, some of which act outside the sandbox
// on its behalf (launchd starting jobs, the pasteboard, Apple Events to
// other apps). These are the ones ordinary command-line tools need:
// user and group lookups, logging, preferences, the per-user temp dir,
// certificate trust for TLS, and file-system events for watchers.
var seatbeltMachServices = []string{
	"com.apple.system.opendirectoryd.libinfo",
	"com.apple.system.opendirectoryd.membership",
	"com.apple.system.DirectoryService.libinfo_v1",
	"com.apple.system.DirectoryService.membership_v1",
	"com.apple.system.logger",
	"com.apple.system.notification_center",
	"com.apple.logd",
	"com.apple.diagnosticd",
	"com.apple.cfprefsd.daemon",
	"com.apple.cfprefsd.agent",
	"com.apple.bsd.dirhelper",
	"com.apple.trustd",
	"com.apple.trustd.agent",
	"com.apple.securityd.xpc",
	"com.apple.SecurityServer",
	"com.apple.ocspd",
	"com.apple.CoreServices.coreservicesd",
	"com.apple.coreservices.launchservicesd",
	"com.apple.lsd.mapdb",
	"com.apple.FSEvents",
}

// seatbeltNetMachServices are needed only to use the network: name
// resolution and the system's network configuration. A profile with no
// network does not get them.
var seatbeltNetMachServices = []string{
	"com.apple.SystemConfiguration.configd",
	"com.apple.SystemConfiguration.DNSConfiguration",
	"com.apple.mDNSResponder",
	"com.apple.dnssd.service",
	"com.apple.networkd",
	"com.apple.usymptomsd",
}

// seatbeltMach is the mach-lookup rule for a profile. extra is
// JANETCODE_SANDBOX_MACH, a comma-separated list of further service
// names for a tool that needs one this list does not have.
func seatbeltMach(network bool, extra string) string {
	names := append([]string(nil), seatbeltMachServices...)
	if network {
		names = append(names, seatbeltNetMachServices...)
	}
	for _, n := range strings.Split(extra, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	var b strings.Builder
	b.WriteString("(allow mach-lookup")
	for _, n := range names {
		b.WriteString("\n  (global-name " + sbString(n) + ")")
	}
	b.WriteString(")\n")
	return b.String()
}
