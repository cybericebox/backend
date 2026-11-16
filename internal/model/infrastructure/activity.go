package infrastructure

import "errors"

// ErrActivityDisabled: the cluster runs without activity reports (the laboratory answers FailedPrecondition, or its features say
// the reports are off). It is not a failure: the figures that depend on them stay "not reported".
var ErrActivityDisabled = errors.New("the laboratory's activity reports are off")

// ActivityPage is one answer of GetLabGroupActivity: the raw facts since a cursor. NextCursor is passed back unchanged; HasMore
// says to ask again at once; Gap says the laboratory dropped events the cursor had not reached, so totals built from them are a
// lower bound.
type ActivityPage struct {
	Events     []ActivityEvent
	NextCursor string
	HasMore    bool
	Gap        bool
}

// ActivityEvent is one raw fact of a lab (exactly one of the three is set). Egress and Touch are deltas of the minute that ends at
// AtMs, Console is a point.
type ActivityEvent struct {
	AtMs    int64
	Lab     string
	Egress  *InternetEgress
	Console *ConsoleSession
	Touch   *DeviceTouch
}

// InternetEgress is the traffic of a lab to the Internet through its gateway. The laboratory does not attribute it to a device
// (the source address is best effort), so nothing here may be mapped to a device or a user; the fact that the lab used the
// Internet is what counts.
type InternetEgress struct {
	SourceAddress, DstAddress string
	DstPort                   int32
	BytesUp, BytesDown        int64
}

// ConsoleSession: ClosedAtMs is 0 on the opening event.
type ConsoleSession struct {
	SessionID, Device, Client string
	OpenedAtMs, ClosedAtMs    int64
}

// DeviceTouch is what one VPN client exchanged with one lab address in a minute (BytesOut from the client, BytesIn from the lab).
type DeviceTouch struct {
	Client, DstAddress string
	BytesOut, BytesIn  int64
}

// ErrActivityGroupGone: the laboratory does not know the group (it was removed, or belongs to another tenant). ErrActivityCursor:
// the laboratory did not give that cursor (another agent, or a restart); the pull starts from the oldest event it still keeps.
var (
	ErrActivityGroupGone = errors.New("the lab group is unknown to the laboratory")
	ErrActivityCursor    = errors.New("the laboratory does not know the activity cursor")
)
