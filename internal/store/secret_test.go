package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func newSecretBackend() (Backend, *fake.Clientset) {
	cs := fake.NewSimpleClientset()

	// The fake client does not stamp resourceVersion, and this backend is built
	// on it: an empty one means "nothing stored yet". Put it back, the way an
	// API server would, or every test here would be exercising the wrong path.
	var rv int64
	stamp := func(action ktesting.Action) (bool, runtime.Object, error) {
		var obj runtime.Object
		switch a := action.(type) {
		case ktesting.CreateActionImpl:
			obj = a.Object
		case ktesting.UpdateActionImpl:
			obj = a.Object
		default:
			return false, nil, nil
		}
		if m, err := meta.Accessor(obj); err == nil {
			rv++
			m.SetResourceVersion(strconv.FormatInt(rv, 10))
		}
		return false, nil, nil // and let the tracker store what we just marked
	}
	cs.PrependReactor("create", "secrets", stamp)
	cs.PrependReactor("update", "secrets", stamp)

	return NewSecretBackend(cs, "devops-tools", "devops-tools-access"), cs
}

func TestAnAbsentSecretReadsAsAFirstStart(t *testing.T) {
	// Told apart from "unreadable", because one creates the store and the other
	// must stop the service.
	b, _ := newSecretBackend()
	if _, _, err := b.Load(context.Background()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestTheSecretRoundTrips(t *testing.T) {
	b, _ := newSecretBackend()
	ctx := context.Background()

	version, err := b.Save(ctx, []byte(`{"version":2}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if version == "" {
		t.Fatal("no version came back; every later write would be treated as a first one")
	}

	data, got, err := b.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"version":2}` {
		t.Fatalf("read back %q", data)
	}
	if got != version {
		t.Fatalf("version %q on read, %q on write", got, version)
	}
}

func TestASecondCreateIsAConflict(t *testing.T) {
	// Two replicas starting together: one creates the object, the other must
	// reload and apply its change rather than fail.
	b, _ := newSecretBackend()
	ctx := context.Background()
	if _, err := b.Save(ctx, []byte("{}"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Save(ctx, []byte("{}"), ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestAStaleWriteIsAConflict(t *testing.T) {
	b, cs := newSecretBackend()
	ctx := context.Background()
	version, err := b.Save(ctx, []byte("{}"), "")
	if err != nil {
		t.Fatal(err)
	}
	// The fake client does not enforce resourceVersion, so the API server's
	// answer is put in by hand: what is being tested here is that this backend
	// turns it into ErrConflict, which is what makes the caller retry.
	cs.PrependReactor("update", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Resource: "secrets"}, "devops-tools-access", fmt.Errorf("stale"))
	})
	if _, err := b.Save(ctx, []byte("{}"), version); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestADeletedSecretIsAConflictRatherThanAFailure(t *testing.T) {
	// Somebody removed it. Reloading finds nothing and writes it back, which is
	// better than refusing the change and leaving the model only in memory.
	b, cs := newSecretBackend()
	ctx := context.Background()
	version, err := b.Save(ctx, []byte("{}"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.CoreV1().Secrets("devops-tools").Delete(ctx, "devops-tools-access", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Save(ctx, []byte("{}"), version); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestTooLargeIsRefusedWithSomethingToDoAboutIt(t *testing.T) {
	// Incompressible on purpose: the ceiling is about what is stored, so a
	// megabyte of zeroes is not too large — it packs away to nothing.
	noise := make([]byte, maxSecretBytes+(1<<16))
	r := rand.New(rand.NewSource(1))
	if _, err := r.Read(noise); err != nil {
		t.Fatal(err)
	}

	b, _ := newSecretBackend()
	_, err := b.Save(context.Background(), noise, "")
	if err == nil {
		t.Fatal("an oversized model was accepted")
	}
	if !strings.Contains(err.Error(), "storage.backend: file") {
		t.Fatalf("the error says what is wrong but not what to do: %v", err)
	}
}

func TestMigrationRunsOnceAndKeepsTheFile(t *testing.T) {
	path := t.TempDir() + "/access.json"
	from, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := from.Invite("anna", "", []string{"readonly"}); err != nil {
		t.Fatal(err)
	}

	b, _ := newSecretBackend()
	ctx := context.Background()

	moved, err := MigrateFile(ctx, path, b)
	if err != nil {
		t.Fatal(err)
	}
	if !moved {
		t.Fatal("nothing was migrated")
	}
	s, err := OpenWith(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.RolesFor("anna"); len(got) != 1 || got[0] != "readonly" {
		t.Fatalf("the migrated model has %v", got)
	}

	// Again: the model is already there, so the file must not be copied over
	// whatever has happened since.
	if err := s.SetRoles("anna", []string{"platform-admin"}); err != nil {
		t.Fatal(err)
	}
	if moved, err := MigrateFile(ctx, path, b); err != nil || moved {
		t.Fatalf("migrated=%v err=%v; the second run should do nothing", moved, err)
	}
	again, err := OpenWith(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.RolesFor("anna"); len(got) != 1 || got[0] != "platform-admin" {
		t.Fatalf("a later change was overwritten by the file: %v", got)
	}

	// And the file is still there, because that is what makes the move
	// reversible.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file was not left alone: %v", err)
	}
}

func TestNothingToMigrateIsNotAnError(t *testing.T) {
	b, _ := newSecretBackend()
	moved, err := MigrateFile(context.Background(), t.TempDir()+"/absent.json", b)
	if err != nil || moved {
		t.Fatalf("migrated=%v err=%v; a fresh installation has nothing to carry over", moved, err)
	}
}

// big is a document large enough to be compressed, and repetitive the way a
// real one is: the same group names over and over.
func big(t *testing.T, users int) []byte {
	t.Helper()
	m := map[string]*User{}
	for i := 0; i < users; i++ {
		name := fmt.Sprintf("surname.name%04d", i)
		m[name] = &User{
			Username: name, Email: name + "@example.kz", Provider: "ldap",
			Roles: []string{"payments-developer"},
			Groups: []string{
				"CN=14. Отдел разработки информационных систем,OU=Distribution_Group,OU=OU_Groups,DC=example,DC=net",
				"CN=39. Команда Инфраструктура,OU=Distribution_Group,OU=OU_Groups,DC=example,DC=net",
			},
		}
	}
	data, err := json.MarshalIndent(encodeFile(m, nil, nil), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestASmallModelIsStoredSoItCanBeRead(t *testing.T) {
	// Below the threshold the object is plain JSON on purpose: it can be opened
	// with kubectl and fixed by hand, which is half the reason for a Secret.
	b, cs := newSecretBackend()
	if _, err := b.Save(context.Background(), []byte(`{"version":3,"users":{}}`), ""); err != nil {
		t.Fatal(err)
	}
	sec, err := cs.CoreV1().Secrets("devops-tools").Get(context.Background(), "devops-tools-access", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if Compressed(sec.Data[secretKey]) {
		t.Fatal("a small model was compressed; it should still be readable")
	}
}

func TestALargeModelIsCompressedAndComesBackIdentical(t *testing.T) {
	b, cs := newSecretBackend()
	ctx := context.Background()
	plain := big(t, 1200)
	if len(plain) < packAbove {
		t.Fatalf("the fixture is only %d bytes, below the threshold", len(plain))
	}

	if _, err := b.Save(ctx, plain, ""); err != nil {
		t.Fatal(err)
	}
	sec, err := cs.CoreV1().Secrets("devops-tools").Get(ctx, "devops-tools-access", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stored := sec.Data[secretKey]
	if !Compressed(stored) {
		t.Fatal("a large model was stored uncompressed")
	}
	if len(stored) >= len(plain) {
		t.Fatalf("compressing made it bigger: %d from %d", len(stored), len(plain))
	}

	back, _, err := b.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatal("what came back is not what went in")
	}
	if m, ok := b.(Measured); !ok || m.StoredBytes() != len(stored) {
		t.Fatal("the stored size is not reported, so the screen would show the wrong number")
	}
}

func TestTheCeilingAppliesToWhatIsStored(t *testing.T) {
	// A model far larger than a Secret holds, which compresses to well under
	// it. Refusing this would be refusing something that fits.
	b, _ := newSecretBackend()
	plain := big(t, 12000)
	if len(plain) < maxSecretBytes {
		t.Fatalf("the fixture is only %d bytes; it needs to exceed the raw ceiling", len(plain))
	}
	if _, err := b.Save(context.Background(), plain, ""); err != nil {
		t.Fatalf("refused a model that fits once stored: %v", err)
	}
}

func TestAPlainDocumentWrittenBeforeAnyOfThisStillOpens(t *testing.T) {
	b, cs := newSecretBackend()
	ctx := context.Background()
	// Put one there the way an older build would have.
	if _, err := cs.CoreV1().Secrets("devops-tools").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "devops-tools-access", Namespace: "devops-tools"},
		Data:       map[string][]byte{secretKey: []byte(`{"version":2,"users":{}}`)},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	data, _, err := b.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"version":2,"users":{}}` {
		t.Fatalf("read back %q", data)
	}
}

func TestACompressedSnapshotCanBeRestored(t *testing.T) {
	// Somebody pulled the copy out of the cluster rather than out of the
	// portal, so it is the packed form.
	b, _ := newSecretBackend()
	s, err := OpenWith(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Invite("anna", "", []string{"readonly"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	var packed bytes.Buffer
	w := gzip.NewWriter(&packed)
	if _, err := w.Write(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteUser("anna"); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(packed.Bytes()); err != nil {
		t.Fatalf("a compressed snapshot was refused: %v", err)
	}
	if _, ok := s.Get("anna"); !ok {
		t.Fatal("the restore did not bring the record back")
	}
}
