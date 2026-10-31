package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	_ "time/tzdata" // distroless images have no zoneinfo

	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/store"
)

// KeyQuiet is the settings key of the quiet hours.
const KeyQuiet = "alerts_quiet"

// DefaultTZ is used for quiet hours and times in Telegram messages until the
// owner picks another zone.
const DefaultTZ = "Europe/Moscow"

// Quiet hours: notifications other than crit are not sent between From and
// To (HH:MM in TZ). Empty From/To disables them.
type Quiet struct {
	From string `json:"from"`
	To   string `json:"to"`
	TZ   string `json:"tz"`
}

// Location returns the time zone of the quiet hours.
func (q Quiet) Location() *time.Location {
	if q.TZ != "" {
		if loc, err := time.LoadLocation(q.TZ); err == nil {
			return loc
		}
	}
	loc, _ := time.LoadLocation(DefaultTZ)
	return loc
}

func parseHM(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, i18n.Errorf("time %q: HH:MM format needed", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Validate checks the format.
func (q Quiet) Validate() error {
	if (q.From == "") != (q.To == "") {
		return errors.New(i18n.T("set both times or neither"))
	}
	if q.From != "" {
		if _, err := parseHM(q.From); err != nil {
			return err
		}
		if _, err := parseHM(q.To); err != nil {
			return err
		}
	}
	if q.TZ != "" {
		if _, err := time.LoadLocation(q.TZ); err != nil {
			return i18n.Errorf("unknown time zone %q", q.TZ)
		}
	}
	return nil
}

// Active reports whether t falls into the quiet hours. The range may cross
// midnight (01:00–07:00 or 23:00–07:00).
func (q Quiet) Active(t time.Time) bool {
	if q.From == "" || q.To == "" {
		return false
	}
	from, err1 := parseHM(q.From)
	to, err2 := parseHM(q.To)
	if err1 != nil || err2 != nil || from == to {
		return false
	}
	lt := t.In(q.Location())
	m := lt.Hour()*60 + lt.Minute()
	if from < to {
		return m >= from && m < to
	}
	return m >= from || m < to
}

// LoadQuiet reads the quiet hours; the default is 01:00–07:00 Moscow time,
// as in the mockup.
func LoadQuiet(ctx context.Context, st *store.Store) (Quiet, error) {
	v, err := st.GetSetting(ctx, KeyQuiet)
	if errors.Is(err, store.ErrNotFound) {
		return Quiet{From: "01:00", To: "07:00", TZ: DefaultTZ}, nil
	}
	if err != nil {
		return Quiet{}, err
	}
	var q Quiet
	if err := json.Unmarshal([]byte(v), &q); err != nil {
		return Quiet{}, err
	}
	return q, nil
}

// SaveQuiet stores the quiet hours.
func SaveQuiet(ctx context.Context, st *store.Store, q Quiet) error {
	b, _ := json.Marshal(q)
	return st.SetSetting(ctx, KeyQuiet, string(b))
}
