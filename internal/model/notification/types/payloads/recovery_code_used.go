package notificationPayloads

import (
	"encoding/json"

	notificationTypes "github.com/cybericebox/backend/internal/model/notification/types"
)

// RecoveryCodeUsedPayload tells the owner that a one-time recovery code signed in to the account, how many codes are
// left and where to manage the second factor (access-control design §6.6).
type RecoveryCodeUsedPayload struct {
	Name        string `var:"Name"        desc:"User name"                         default:"John Doe"`
	CodesLeft   string `var:"CodesLeft"   desc:"How many recovery codes are left"  default:"9"`
	SecurityURL string `var:"SecurityURL" desc:"Account security settings link"   default:"https://id.example.org/profile/"`
}

func (RecoveryCodeUsedPayload) NotificationType() notificationTypes.NotificationType {
	return notificationTypes.NotificationTypeRecoveryCodeUsed
}
func (RecoveryCodeUsedPayload) NotificationChannels() []notificationTypes.NotificationChannel {
	return []notificationTypes.NotificationChannel{notificationTypes.NotificationChannelEmail}
}
func (p RecoveryCodeUsedPayload) Marshal() (json.RawMessage, error) { return json.Marshal(p) }

func init() { notificationTypes.Register(RecoveryCodeUsedPayload{}) }
