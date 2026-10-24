package store

// User is an account on this box.
type User struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"` // admin | member
	CreatedAt    string `json:"created_at"`
}

// APIToken is a personal access token (hash stored, plaintext shown once).
type APIToken struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
}

// Connection is how a person reaches Audiobookshelf.
type Connection struct {
	UserID        int64  `json:"-"`
	Kind          string `json:"kind"` // key | direct | core | off
	ServerURL     string `json:"server_url"`
	Username      string `json:"username"`
	ABSUserID     string `json:"abs_user_id"`
	Secret        string `json:"-"` // sealed API key or refresh token
	Access        string `json:"-"` // sealed access token (password sign-ins)
	ServerVersion string `json:"server_version"`
	UpdatedAt     string `json:"updated_at"`
}

// Listen is one listening session (from Audiobookshelf: this app's or any other's).
type Listen struct {
	ID        int64    `json:"id"`
	ABSID     string   `json:"abs_id"`
	ItemID    string   `json:"item_id"`
	LibraryID string   `json:"library_id"`
	Title     string   `json:"title"`
	Author    string   `json:"author"`
	Narrator  string   `json:"narrator"`
	Series    string   `json:"series"`
	Genres    []string `json:"genres"`
	Duration  int      `json:"duration"`
	Day       string   `json:"day"`
	StartedAt string   `json:"started_at"`
	UpdatedAt string   `json:"updated_at"`
	Seconds   int      `json:"seconds"`
	StartPos  int      `json:"start_pos"`
	EndPos    int      `json:"end_pos"`
	Device    string   `json:"device"`
	Source    string   `json:"source"`
}

// Finish is a book a person finished.
type Finish struct {
	ID         int64  `json:"id"`
	ItemID     string `json:"item_id"`
	Title      string `json:"title"`
	Author     string `json:"author"`
	Duration   int    `json:"duration"`
	FinishedAt string `json:"finished_at"`
	Day        string `json:"day"`
}

// SyncState is where reading a person's history from Audiobookshelf stands.
type SyncState struct {
	ServerKey  string `json:"-"`
	Watermark  int64  `json:"watermark"`
	Backfilled bool   `json:"backfilled"`
	SyncedAt   string `json:"synced_at"`
	Error      string `json:"error"`
}

// UpNext is a book a person wants to listen to next.
type UpNext struct {
	ID       int64  `json:"id"`
	ItemID   string `json:"item_id"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Position int    `json:"position"`
	AddedAt  string `json:"added_at"`
}

// Settings are the box-wide title plus one person's preferences.
type Settings struct {
	Title         string `json:"title"`
	SetupComplete bool   `json:"setup_complete"`

	DateFormat string `json:"date_format"`
	TimeFormat string `json:"time_format"`

	// Playback.
	Speed           float64 `json:"speed"`            // default playback speed
	SkipBack        int     `json:"skip_back"`        // seconds
	SkipForward     int     `json:"skip_forward"`     // seconds
	RewindOnResume  int     `json:"rewind_on_resume"` // seconds to go back after a long pause
	PlayNext        bool    `json:"play_next"`        // a finished book goes on with Up next or the next in its series
	HiddenLibraries string  `json:"hidden_libraries"` // comma list of library ids left out of home and browse

	// Goals: minutes a day (0 = none).
	DailyGoal int `json:"daily_goal"`

	// Pushes: a finished book; once a week a recap.
	NotifyURL      string `json:"notify_url"`
	NotifyFormat   string `json:"notify_format"` // apprise | ntfy | json
	NotifyFinished bool   `json:"notify_finished"`
	NotifyWeekly   bool   `json:"notify_weekly"`
}
