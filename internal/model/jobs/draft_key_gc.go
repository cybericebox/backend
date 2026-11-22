package jobsModel

// DraftKeyGCArgs is the empty payload of the periodic deletion of the draft keys nobody used for their lifetime.
type DraftKeyGCArgs struct{}

func (DraftKeyGCArgs) Kind() string { return "draft_key_gc" }
