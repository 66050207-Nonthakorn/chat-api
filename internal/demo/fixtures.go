package demo

// Fixed IDs keep local fixtures reproducible across seeds and API instances.
const (
	UserAID      = "11111111-1111-4111-8111-111111111111"
	UserBID      = "22222222-2222-4222-8222-222222222222"
	GroupRoomID  = "33333333-3333-4333-8333-333333333333"
	DirectRoomID = "44444444-4444-4444-8444-444444444444"
)

type User struct {
	ID          string
	DisplayName string
}

func UserByID(id string) (User, bool) {
	switch id {
	case UserAID:
		return User{ID: UserAID, DisplayName: "Demo A"}, true
	case UserBID:
		return User{ID: UserBID, DisplayName: "Demo B"}, true
	default:
		return User{}, false
	}
}
