package labActivityRepo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
)

type fake struct {
	Queries
	cursorErr error
	applied   postgres.ApplyLabActivityParams
	groups    postgres.ListLabActivityGroupsParams
	instances postgres.ListAssistantLabActivityParams
	rows      []postgres.ListAssistantLabActivityRow
	client    postgres.GetLabActivityOfClientRow
	clientErr error
}

func (f *fake) ListLabActivityGroups(_ context.Context, p postgres.ListLabActivityGroupsParams) ([]string, error) {
	f.groups = p
	return []string{"g-1"}, nil
}

func (f *fake) GetLabActivityCursor(context.Context, string) (postgres.GetLabActivityCursorRow, error) {
	return postgres.GetLabActivityCursorRow{Cursor: "c"}, f.cursorErr
}

func (f *fake) ApplyLabActivity(_ context.Context, p postgres.ApplyLabActivityParams) error {
	f.applied = p
	return nil
}

func (f *fake) ListAssistantLabActivity(_ context.Context, p postgres.ListAssistantLabActivityParams) ([]postgres.ListAssistantLabActivityRow, error) {
	f.instances = p
	return f.rows, nil
}

func (f *fake) GetLabActivityOfClient(context.Context, postgres.GetLabActivityOfClientParams) (postgres.GetLabActivityOfClientRow, error) {
	return f.client, f.clientErr
}

func TestApplyHandsTheBatchAndTheCursorToOneStatement(t *testing.T) {
	f := &fake{}
	now := time.Unix(100, 0)
	err := New(f).Apply(context.Background(), "g-1", "opaque==", true, Batch{
		Labs: []Lab{{Lab: "l-1", Egress: true}}, Clients: []Client{{Lab: "l-1", Client: "p1", Bytes: 9}},
		Dsts: []Dst{{Lab: "l-1", Client: "p1", Dst: "10.0.0.7"}}, Sessions: []Session{{Lab: "l-1", SessionID: "s", Client: "p1", OpenedMs: 1, ClosedMs: 0}},
	}, now)
	require.NoError(t, err)
	assert.Equal(t, "opaque==", f.applied.Cursor, "the cursor is stored as the laboratory gave it")
	assert.True(t, f.applied.Gap)
	var got map[string][]map[string]any
	require.NoError(t, json.Unmarshal(f.applied.Batch, &got))
	assert.Equal(t, true, got["labs"][0]["egress"])
	assert.EqualValues(t, 9, got["clients"][0]["bytes"])
	assert.Equal(t, "10.0.0.7", got["dsts"][0]["dst"])
	assert.Contains(t, got["sessions"][0], "session_id")
	// no key of the batch names a device
	for _, list := range got {
		for _, entry := range list {
			assert.NotContains(t, entry, "device")
		}
	}
}

func TestEmptyBatchListsAreArraysForTheStatement(t *testing.T) {
	f := &fake{}
	require.NoError(t, New(f).Apply(context.Background(), "g", "", false, Batch{}, time.Now()))
	// jsonb_array_elements(NULL) yields no rows, but 'null' is a scalar that errors: the lists must be arrays or absent
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(f.applied.Batch, &raw))
	for key, v := range raw {
		assert.NotEqual(t, "null", string(v), key)
	}
}

func TestGroupsAndCursor(t *testing.T) {
	f := &fake{}
	r := New(f)
	groups, err := r.Groups(context.Background(), assistantModel.StatsModeContinuous, time.Unix(5, 0))
	require.NoError(t, err)
	assert.Equal(t, []string{"g-1"}, groups)
	assert.EqualValues(t, 3, f.groups.DefaultMode)

	c, found, err := r.Cursor(context.Background(), "g-1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "c", c)
	f.cursorErr = pgx.ErrNoRows
	_, found, err = r.Cursor(context.Background(), "g-1")
	require.NoError(t, err)
	assert.False(t, found, "a group never pulled")
}

func TestInstancesConvertSecondsToMinutesAndNeverSendNullLists(t *testing.T) {
	f := &fake{rows: []postgres.ListAssistantLabActivityRow{{EventID: uuid.Nil, VpnBytes: 5, DevicesTouched: 2, ConsoleSeconds: 150, EgressUsed: true}}}
	got, err := New(f).Instances(context.Background(), nil, nil, time.Unix(1, 0), 4*time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.InDelta(t, 2.5, got[0].ConsoleMinutes, 1e-9)
	assert.True(t, got[0].EgressUsed)
	assert.NotNil(t, f.instances.TeamIds, "a nil slice is NULL in SQL")
	assert.NotNil(t, f.instances.ExerciseIds)
	assert.InDelta(t, 4*3600, f.instances.MaxSessionSeconds, 1e-9)
}

func TestOfClientReportsAMissingLabAsNotFound(t *testing.T) {
	f := &fake{clientErr: pgx.ErrNoRows}
	_, found, err := New(f).OfClient(context.Background(), "g", "l", "c", time.Now(), time.Hour)
	require.NoError(t, err)
	assert.False(t, found)
	f.clientErr, f.client = nil, postgres.GetLabActivityOfClientRow{EgressUsed: true, VpnBytes: 3, ConsoleSeconds: 60}
	got, found, err := New(f).OfClient(context.Background(), "g", "l", "c", time.Now(), time.Hour)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, Figures{EgressUsed: true, VPNBytes: 3, ConsoleMinutes: 1}, got)
}
