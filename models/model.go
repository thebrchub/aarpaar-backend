package models

import "time"

type User struct {
	ID        string    `db:"id" json:"id"`
	GoogleID  string    `db:"google_id" json:"-"`
	Email     string    `db:"email" json:"email"`
	Name      string    `db:"name" json:"name"`
	Username  *string   `db:"username" json:"username"`
	AvatarURL string    `db:"avatar_url" json:"avatar_url"`
	Mobile    *string   `db:"mobile" json:"mobile"`
	Gender    string    `db:"gender" json:"gender"`
	IsPrivate bool      `db:"is_private" json:"is_private"`
	IsBanned  bool      `db:"is_banned" json:"-"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}
