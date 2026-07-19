package sharedvolume

import (
	"context"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

type fakeProjects struct{ err error }

func (f fakeProjects) GetProject(_ context.Context, name string, _ []string) (descriptor.Descriptor, error) {
	if f.err != nil {
		return descriptor.Descriptor{}, f.err
	}
	return descriptor.Descriptor{ProjectName: name, Namespace: "ns-" + name}, nil
}

type fakeNomad struct {
	created   [][2]string // {namespace, name}
	deleted   [][2]string
	createErr error
}

func (f *fakeNomad) CreateSharedVolume(namespace, name, _ string) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, [2]string{namespace, name})
	return nil
}

func (f *fakeNomad) DeleteHostVolume(namespace, name string) error {
	f.deleted = append(f.deleted, [2]string{namespace, name})
	return nil
}

func newSvc(enabled bool) (*Service, *fakeNomad) {
	fn := &fakeNomad{}
	svc := New(store.NewMemory(), fakeProjects{}, fn, Config{Enabled: enabled, FilesystemID: "fs-123"})
	return svc, fn
}

func TestCreateProvisionsVolumeAndRow(t *testing.T) {
	svc, fn := newSvc(true)
	v, err := svc.Create(context.Background(), "alice@x", nil, "acme", CreateInput{Name: "cache", MountPath: "/shared/cache"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if v.VolumeID != "shared-acme-cache" {
		t.Errorf("volume id = %q, want shared-acme-cache", v.VolumeID)
	}
	if len(fn.created) != 1 || fn.created[0] != [2]string{"ns-acme", "shared-acme-cache"} {
		t.Errorf("nomad create calls = %v", fn.created)
	}
	got, err := svc.List(context.Background(), nil, "acme")
	if err != nil || len(got) != 1 || got[0].Name != "cache" {
		t.Fatalf("list = %v, err %v", got, err)
	}
}

func TestCreateDefaultsMountPath(t *testing.T) {
	svc, _ := newSvc(true)
	v, err := svc.Create(context.Background(), "a", nil, "acme", CreateInput{Name: "data"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if v.MountPath != "/shared/data" {
		t.Errorf("mount path = %q, want /shared/data", v.MountPath)
	}
}

func TestCreateDisabled(t *testing.T) {
	svc, _ := newSvc(false)
	_, err := svc.Create(context.Background(), "a", nil, "acme", CreateInput{Name: "cache"})
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("want ErrBadRequest, got %v", err)
	}
}

func TestCreateRejectsBadNameAndPath(t *testing.T) {
	svc, _ := newSvc(true)
	for _, tc := range []CreateInput{
		{Name: "Bad_Name"},              // uppercase + underscore
		{Name: "x"},                     // too short
		{Name: "ok", MountPath: "/etc"}, // outside /shared
		{Name: "ok", MountPath: "/shared/../etc"},
	} {
		if _, err := svc.Create(context.Background(), "a", nil, "acme", tc); !errors.Is(err, apperr.ErrBadRequest) {
			t.Errorf("input %+v: want ErrBadRequest, got %v", tc, err)
		}
	}
}

func TestCreateDuplicateConflicts(t *testing.T) {
	svc, _ := newSvc(true)
	if _, err := svc.Create(context.Background(), "a", nil, "acme", CreateInput{Name: "cache"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := svc.Create(context.Background(), "a", nil, "acme", CreateInput{Name: "cache"})
	if !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestDeleteRemovesVolumeAndRow(t *testing.T) {
	svc, fn := newSvc(true)
	if _, err := svc.Create(context.Background(), "a", nil, "acme", CreateInput{Name: "cache"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Delete(context.Background(), nil, "acme", "cache"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(fn.deleted) != 1 || fn.deleted[0] != [2]string{"ns-acme", "shared-acme-cache"} {
		t.Errorf("nomad delete calls = %v", fn.deleted)
	}
	if err := svc.Delete(context.Background(), nil, "acme", "cache"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("second delete: want ErrNotFound, got %v", err)
	}
}
