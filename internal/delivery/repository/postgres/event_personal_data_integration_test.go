package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/repository/eventTeamRepo"
	"github.com/cybericebox/backend/internal/delivery/repository/participantRepo"
	eventTeamModel "github.com/cybericebox/backend/internal/model/eventTeam"
	dispatchModel "github.com/cybericebox/backend/internal/model/notification/dispatch"
	"github.com/cybericebox/backend/internal/testhelpers"
)

// For an observer (HideEmails) the manage lists never read an address in full: the search does not match it, and
// the table columns that hold one (@email, @name of a pending invitation, the teams' @captain) carry it masked,
// as the observer sees it, so a filter or a sort cannot find out more than the screen shows.
func TestManageListsKeepEmailsOutOfObserverQueries(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	repo := participantRepo.New(db.Queries)
	event := mustSeedEventForParticipants(t, db, "pdata")
	named := mustSeedUser(t, db, "secret-olena@pdata.test")
	invited := mustSeedUser(t, db, "hidden-taras@pdata.test")
	for i, user := range []uuid.UUID{named, invited} {
		if _, _, err := repo.Upsert(ctx, mustNewPendingParticipant(t, event.ID, user, epNow.Add(time.Duration(i)*time.Hour))); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	rtExec(t, db, `UPDATE users SET first_name = 'Olena', last_name = 'Shevchenko' WHERE id = $1`, named)
	rtExec(t, db, `UPDATE event_participants SET invited = true WHERE event_id = $1 AND user_id = $2`, event.ID, invited)

	count := func(search string, hide bool) int64 {
		t.Helper()
		n, err := repo.CountMatching(ctx, event.ID, -1, participantRepo.KindAll, search, nil, hide)
		if err != nil {
			t.Fatalf("CountMatching: %v", err)
		}
		rows, err := repo.List(ctx, event.ID, -1, participantRepo.KindAll, search, nil, epCursorSentinelTime, epMaxUUID, 10, hide)
		if err != nil || int64(len(rows)) != n {
			t.Fatalf("List = %d rows err=%v, count %d", len(rows), err, n)
		}
		return n
	}
	if got := count("secret-olena", false); got != 1 {
		t.Fatalf("a manager finds the address: %d", got)
	}
	if got := count("secret-olena", true); got != 0 {
		t.Fatalf("an observer finds the address: %d", got)
	}
	if got := count("Olena", true); got != 1 {
		t.Fatalf("an observer finds the name: %d", got)
	}

	table := func(filters, sortKey string, hide bool) int64 {
		t.Helper()
		query := participantRepo.TableQuery{EventID: event.ID, StatusFilter: -1, Kind: participantRepo.KindAll, Filters: []byte(filters), SortKey: sortKey, Limit: 10, HideEmails: hide}
		rows, err := repo.ListTable(ctx, query)
		if err != nil {
			t.Fatalf("ListTable: %v", err)
		}
		n, err := repo.CountTable(ctx, query)
		if err != nil || n != int64(len(rows)) {
			t.Fatalf("CountTable = %d err=%v, rows %d", n, err, len(rows))
		}
		return n
	}
	for _, tc := range []struct {
		name, filters string
		manager, obs  int64
	}{
		{"search the address through @email", `[{"key":"@email","op":"contains","value":"secret"}]`, 1, 0},
		{"the masked address is what an observer sees", `[{"key":"@email","op":"contains","value":"s***@pdata.test"}]`, 0, 1},
		{"a pending invitation's @name is its address", `[{"key":"@name","op":"contains","value":"hidden-taras"}]`, 1, 0},
		{"no filter lists everyone", `[]`, 2, 2},
		{"@name of an applicant is the profile name", `[{"key":"@name","op":"contains","value":"Olena"}]`, 1, 1},
		{"@name of an applicant never holds the address", `[{"key":"@name","op":"contains","value":"secret"}]`, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := table(tc.filters, "@email", false); got != tc.manager {
				t.Fatalf("manager rows = %d, want %d", got, tc.manager)
			}
			if got := table(tc.filters, "@email", true); got != tc.obs {
				t.Fatalf("observer rows = %d, want %d", got, tc.obs)
			}
		})
	}
	searched := participantRepo.TableQuery{EventID: event.ID, StatusFilter: -1, Kind: participantRepo.KindAll, Search: "hidden-taras", Filters: []byte("[]"), SortKey: "@date", Limit: 10, HideEmails: true}
	if n, err := repo.CountTable(ctx, searched); err != nil || n != 0 {
		t.Fatalf("observer table search by address = %d err=%v", n, err)
	}

	teams := eventTeamRepo.New(db.Queries)
	team, err := eventTeamModel.New(event.ID, named, "Owls", "pdata-code-0", epNow)
	if err != nil {
		t.Fatalf("New team: %v", err)
	}
	if _, err = teams.Create(ctx, team); err != nil {
		t.Fatalf("Create team: %v", err)
	}
	captain := func(value string, hide bool) int64 {
		t.Helper()
		query := eventTeamRepo.TableQuery{EventID: event.ID, Filters: []byte(`[{"key":"@captain","op":"contains","value":"` + value + `"}]`), SortKey: "@captain", Limit: 10, HideEmails: hide}
		rows, lErr := teams.ListTable(ctx, query)
		if lErr != nil {
			t.Fatalf("teams ListTable: %v", lErr)
		}
		return int64(len(rows))
	}
	if captain("secret-olena", false) != 1 || captain("secret-olena", true) != 0 || captain("Olena", true) != 1 {
		t.Fatal("an observer filters the teams by the captain's address")
	}
}

// The SQL mask of the observer's columns (event_mask_email) is dispatch.MaskEmail, which masks what the screen shows,
// on ordinary and odd addresses alike.
func TestEventMaskEmailEqualsDispatchMaskEmail(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	for _, email := range []string{
		"olena@example.com", "o@example.com", "Олена@приклад.укр", "😀smile@example.com",
		"a@b@example.com", "@example.com", "olena@", "no-at-sign", "",
	} {
		t.Run(email, func(t *testing.T) {
			var got string
			if err := db.Pool.QueryRow(context.Background(), `SELECT event_mask_email($1)`, email).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if want := dispatchModel.MaskEmail(email); got != want {
				t.Fatalf("event_mask_email(%q) = %q, dispatch.MaskEmail = %q", email, got, want)
			}
		})
	}
}

// An observer's sort by an address column follows the masked address, so the order tells nothing the screen does
// not: «ab@a…» (a***@a…) comes before «aa@b…» (a***@b…), the reverse of the full addresses.
func TestObserverSortsByTheMaskedAddress(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	repo := participantRepo.New(db.Queries)
	teams := eventTeamRepo.New(db.Queries)
	event := mustSeedEventForParticipants(t, db, "pdsort")
	first := mustSeedUser(t, db, "aa@b.pdsort.test")
	second := mustSeedUser(t, db, "ab@a.pdsort.test")
	names := map[uuid.UUID]string{first: "aa", second: "ab"}
	for i, user := range []uuid.UUID{first, second} {
		if _, _, err := repo.Upsert(ctx, mustNewPendingParticipant(t, event.ID, user, epNow.Add(time.Duration(i)*time.Hour))); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		team, err := eventTeamModel.New(event.ID, user, "Team "+names[user], "pdsort-code-"+names[user], epNow.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("New team: %v", err)
		}
		if _, err = teams.Create(ctx, team); err != nil {
			t.Fatalf("Create team: %v", err)
		}
	}
	participants := func(hide bool) string {
		t.Helper()
		rows, err := repo.ListTable(ctx, participantRepo.TableQuery{EventID: event.ID, StatusFilter: -1, Kind: participantRepo.KindAll, Filters: []byte("[]"), SortKey: "@email", Limit: 10, HideEmails: hide})
		if err != nil {
			t.Fatalf("ListTable: %v", err)
		}
		out := ""
		for _, row := range rows {
			out += names[row.Participant.UserID] + " "
		}
		return out
	}
	captains := func(hide bool) string {
		t.Helper()
		rows, err := teams.ListTable(ctx, eventTeamRepo.TableQuery{EventID: event.ID, Filters: []byte("[]"), SortKey: "@captain", Limit: 10, HideEmails: hide})
		if err != nil {
			t.Fatalf("teams ListTable: %v", err)
		}
		out := ""
		for _, row := range rows {
			out += row.Team.Name + " "
		}
		return out
	}
	if got := participants(false); got != "aa ab " {
		t.Fatalf("manager sort by @email = %q", got)
	}
	if got := participants(true); got != "ab aa " {
		t.Fatalf("observer sort by @email = %q", got)
	}
	if got := captains(false); got != "Team aa Team ab " {
		t.Fatalf("manager sort by @captain = %q", got)
	}
	if got := captains(true); got != "Team ab Team aa " {
		t.Fatalf("observer sort by @captain = %q", got)
	}
}
