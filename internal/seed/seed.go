package seed

import (
	"context"
	"fmt"

	"cloudchat/internal/demo"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	transaction, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("could not begin demo seed")
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()

	for _, user := range []demo.User{
		{ID: demo.UserAID, DisplayName: "Demo A"},
		{ID: demo.UserBID, DisplayName: "Demo B"},
	} {
		if _, err = transaction.Exec(ctx, `
			INSERT INTO app_users (user_id, display_name)
			VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE
			SET display_name = EXCLUDED.display_name, is_active = true`, user.ID, user.DisplayName); err != nil {
			return fmt.Errorf("could not seed demo user")
		}
	}

	if _, err = transaction.Exec(ctx, `
		INSERT INTO rooms (room_id, kind, title, owner_user_id)
		VALUES ($1, 'group', 'Demo group', $2)
		ON CONFLICT (room_id) DO UPDATE
		SET kind = EXCLUDED.kind, title = EXCLUDED.title, owner_user_id = EXCLUDED.owner_user_id`,
		demo.GroupRoomID, demo.UserAID); err != nil {
		return fmt.Errorf("could not seed demo group")
	}

	if _, err = transaction.Exec(ctx, `
		INSERT INTO rooms (room_id, kind, title, dm_user_low, dm_user_high)
		VALUES ($1, 'dm', 'Demo A and Demo B', $2, $3)
		ON CONFLICT (room_id) DO UPDATE
		SET kind = EXCLUDED.kind, title = EXCLUDED.title,
			dm_user_low = EXCLUDED.dm_user_low, dm_user_high = EXCLUDED.dm_user_high`,
		demo.DirectRoomID, demo.UserAID, demo.UserBID); err != nil {
		return fmt.Errorf("could not seed demo direct room")
	}

	for _, membership := range []struct {
		roomID string
		userID string
	}{
		{demo.GroupRoomID, demo.UserAID},
		{demo.GroupRoomID, demo.UserBID},
		{demo.DirectRoomID, demo.UserAID},
		{demo.DirectRoomID, demo.UserBID},
	} {
		if _, err = transaction.Exec(ctx, `
			INSERT INTO room_memberships (room_id, user_id, member_role)
			VALUES ($1, $2, 'member')
			ON CONFLICT (room_id, user_id) DO UPDATE
			SET member_role = EXCLUDED.member_role`, membership.roomID, membership.userID); err != nil {
			return fmt.Errorf("could not seed demo room membership")
		}
	}

	if err = transaction.Commit(ctx); err != nil {
		return fmt.Errorf("could not commit demo seed")
	}
	return nil
}
