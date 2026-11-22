// Package labAccessModel defines the transport-neutral desired Laboratory ACL.
package labAccessModel

// ClientPolicy is a complete replacement ACL for one LabGroup VPN client.
type ClientPolicy struct {
	Name        string
	AllowedLabs []string
	// Control is the one address of one control gateway of one lab that the client may reach (the client of a bot's run). A
	// client with it has no AllowedLabs: a bot sees nothing else.
	Control *ControlAccess
}

// ControlAccess is access to one host of one control gateway of a lab: a /32 of the gateway's /24. Every tools device of a lab
// has a control gateway of its own, so Gateway names the one of the bot's device.
type ControlAccess struct {
	Lab     string
	Gateway string
	Host    int32
}
