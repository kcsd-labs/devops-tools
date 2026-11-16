package helmreq

import (
	"context"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

func stores() map[string]func(now func() time.Time) Store {
	return map[string]func(now func() time.Time) Store{
		"memory": func(now func() time.Time) Store { return &memory{reqs: map[string]*Request{}, now: now} },
		"secrets": func(now func() time.Time) Store {
			return &secrets{client: fake.NewSimpleClientset(), namespace: "devops-tools", now: now}
		},
	}
}

func TestRequestLifecycle(t *testing.T) {
	for name, open := range stores() {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
			clock := func() time.Time { return now }
			st := open(clock)

			r := &Request{Kind: KindInstall, Namespace: "payments-dev", Release: "redis", Chart: "redis",
				Values: "auth:\n  password: s3cret\n", Requester: "anna",
				Created: now, Expires: now.Add(7 * 24 * time.Hour), Status: StatusWaiting}
			if err := st.Create(ctx, r); err != nil || r.ID == "" {
				t.Fatalf("create: %v, id %q", err, r.ID)
			}
			got, err := st.Get(ctx, r.ID)
			if err != nil || got.Values != r.Values || !got.Open() {
				t.Fatalf("get: %+v %v", got, err)
			}

			upd, err := st.Update(ctx, r.ID, func(q *Request) error {
				q.Status, q.Decider = StatusInstalling, "admin"
				return nil
			})
			if err != nil || upd.Status != StatusInstalling {
				t.Fatalf("update: %+v %v", upd, err)
			}
			if _, err := st.Get(ctx, "nope"); err != ErrNotFound {
				t.Errorf("unknown id: %v", err)
			}

			// A second request, left waiting past its time, expires.
			late := &Request{Kind: KindInstall, Namespace: "payments-dev", Release: "kafka", Requester: "bob",
				Created: now, Expires: now.Add(7 * 24 * time.Hour), Status: StatusWaiting}
			_ = st.Create(ctx, late)
			now = now.Add(8 * 24 * time.Hour)
			list, err := st.List(ctx)
			if err != nil || len(list) != 2 {
				t.Fatalf("list: %d %v", len(list), err)
			}
			for _, q := range list {
				if q.ID == late.ID && q.Status != StatusExpired {
					t.Errorf("a request past its time should read as expired, got %s", q.Status)
				}
				if q.ID == r.ID && q.Status != StatusInstalling {
					t.Errorf("an install in progress does not expire, got %s", q.Status)
				}
			}

			// Long after it was answered, a request is dropped.
			_, _ = st.Update(ctx, r.ID, func(q *Request) error {
				q.Status = StatusInstalled
				d := now
				q.Decided = &d
				return nil
			})
			now = now.Add(100 * 24 * time.Hour)
			list, _ = st.List(ctx)
			if len(list) != 0 {
				t.Errorf("answered requests older than 90 days should be gone, %d left", len(list))
			}
		})
	}
}
