package jobsModel

// MediaEncryptArgs is the (empty) payload of the job that encrypts the objects stored plain before file encryption existed.
type MediaEncryptArgs struct{}

func (MediaEncryptArgs) Kind() string { return "media_encrypt_legacy" }
