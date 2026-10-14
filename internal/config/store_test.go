package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mycdn/internal/rules"
)

func TestAtomicSaveSnapshotAndDomainPriority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	d := store.Get().Document()
	d.Sites = []Site{
		{ID: "wild", Enabled: true, Domains: []string{"*.example.test"}, Origins: []Origin{{ID: "one", URL: "http://127.0.0.1:9000"}}, DefaultOrigin: "one"},
		{ID: "exact", Enabled: true, Domains: []string{"WWW.Example.Test."}, Origins: []Origin{{ID: "two", URL: "https://example.org"}}, DefaultOrigin: "two"},
	}
	if err := store.Save(d); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Get()
	revision := snapshot.Revision()
	if site, ok := snapshot.MatchSite("www.example.test"); !ok || site.ID != "exact" {
		t.Fatalf("exact match = %+v %v", site, ok)
	}
	if site, ok := snapshot.MatchSite("deep.a.example.test"); !ok || site.ID != "wild" {
		t.Fatalf("wildcard match = %+v %v", site, ok)
	}
	if _, ok := snapshot.MatchSite("example.test"); ok {
		t.Fatal("wildcard matched apex")
	}
	d.Sites[0].Domains[0] = "mutated.test"
	copy := snapshot.Document()
	copy.Sites[1].Domains[0] = "mutated.test"
	if _, ok := snapshot.MatchSite("www.example.test"); !ok {
		t.Fatal("snapshot aliased document")
	}
	invalid := snapshot.Document()
	invalid.Defaults.Cache.Mode = "invented"
	if err := store.Save(invalid); err == nil {
		t.Fatal("invalid save succeeded")
	}
	if store.Get().Revision() != revision {
		t.Fatal("invalid save changed active snapshot")
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Get().Revision() != revision {
		t.Fatal("persisted document differs")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions = %o", info.Mode().Perm())
	}
}

func TestConcurrentPublicationAndIndependentPlans(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	d.Defaults.Delivery.Headers = []rules.HeaderMutation{{Name: "X-Default", Operation: "set", Value: "original"}}
	if err := store.Save(d); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				p := store.Get().Plans()
				p.Delivery.Headers[0].Value = "local"
				if store.Get().Plans().Delivery.Headers[0].Value != "original" {
					t.Error("plans share headers")
				}
			}
		})
	}
	for i := 0; i < 4; i++ {
		if err := store.Save(d); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestDecodeIsStrict(t *testing.T) {
	d := Default()
	data, _ := json.Marshal(d)
	if _, err := Decode(strings.NewReader(string(data))); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(strings.NewReader(strings.Replace(string(data), `"schema_version":1`, `"schema_version":1,"typo":true`, 1))); err == nil {
		t.Fatal("accepted unknown config field")
	}
	if _, err := Decode(strings.NewReader(string(data) + " {}")); err == nil {
		t.Fatal("accepted trailing document")
	}
}

func TestIdentityListPersistsUnsetVersusExplicitEmpty(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if store.Get().Plans().Request.Identity.ClientIPHeaders != nil {
		t.Fatal("default must use preset, not explicit list")
	}
	d := store.Get().Document()
	d.Defaults.Identity.ClientIPHeaders = []rules.ClientIPHeader{}
	if err := store.Save(d); err != nil {
		t.Fatal(err)
	}
	if list := store.Get().Plans().Request.Identity.ClientIPHeaders; list == nil || len(list) != 0 {
		t.Fatal("empty peer-only source list was lost")
	}
	data, _ := json.Marshal(store.Get().Document())
	if !strings.Contains(string(data), `"client_ip_headers":[]`) {
		t.Fatalf("empty list omitted from JSON: %s", data)
	}
	d.Defaults.Identity.ClientIPHeaders = []rules.ClientIPHeader{{Name: "X-Real-IP", Format: "single"}}
	if err := store.Save(d); err != nil {
		t.Fatal(err)
	}
	plans := store.Get().Plans()
	plans.Request.Identity.ClientIPHeaders[0].Name = "X-Changed"
	if store.Get().Plans().Request.Identity.ClientIPHeaders[0].Name != "X-Real-IP" {
		t.Fatal("snapshot aliases identity source plan")
	}
}

func TestRuntimeBudgetsAndH2CValidation(t *testing.T) {
	d := Default()
	if d.Defaults.Upstream.Timeouts.RequestMS != 0 || d.Storage.MaxReaders != 4096 || d.Storage.FillTimeoutMS != 1800000 {
		t.Fatalf("runtime defaults = %+v %+v", d.Defaults.Upstream, d.Storage)
	}
	for _, test := range []struct {
		name, path string
		edit       func(*Document)
	}{
		{"memory", "analytics.memory_bytes", func(d *Document) { d.Analytics.MemoryBytes = 32<<20 - 1 }},
		{"disk", "analytics.max_bytes", func(d *Document) { d.Analytics.MaxBytes = 32<<20 - 1 }},
		{"temp", "analytics.temp_bytes", func(d *Document) { d.Analytics.TempBytes = d.Analytics.MaxBytes/8 + 1 }},
		{"detail", "analytics.detail_days", func(d *Document) { d.Analytics.DetailDays = 0 }},
		{"retention", "analytics.rollup_days", func(d *Document) { d.Analytics.RollupDays = 367 }},
		{"queue", "analytics.queue_size", func(d *Document) { d.Analytics.QueueSize = 1<<20 + 1 }},
		{"batch", "analytics.batch_size", func(d *Document) { d.Analytics.BatchSize = 8193 }},
		{"flush", "analytics.flush_interval_ms", func(d *Document) { d.Analytics.FlushIntervalMS = 60001 }},
		{"queries", "analytics.query_concurrency", func(d *Document) { d.Analytics.QueryConcurrency = 9 }},
		{"timeout", "analytics.query_timeout_ms", func(d *Document) { d.Analytics.QueryTimeoutMS = 120001 }},
		{"threads", "analytics.threads", func(d *Document) { d.Analytics.Threads = 17 }},
		{"cache memory", "storage.memory_bytes", func(d *Document) { d.Storage.MemoryBytes = 0 }},
		{"background", "storage.max_background", func(d *Document) { d.Storage.MaxBackground = 0 }},
		{"inline", "storage.inline_bytes", func(d *Document) { d.Storage.InlineBytes = 0 }},
		{"readers", "storage.max_readers", func(d *Document) { d.Storage.MaxReaders = 0 }},
		{"fill", "storage.fill_timeout_ms", func(d *Document) { d.Storage.FillTimeoutMS = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := Default()
			test.edit(&d)
			_, err := Validate(d)
			var validation *rules.ValidationError
			if !errors.As(err, &validation) || validation.Path != test.path {
				t.Fatalf("validation = %v, expected %s", err, test.path)
			}
		})
	}
	d.Analytics.MemoryBytes = 32 << 20
	d.Analytics.MaxBytes = 32 << 20
	d.Analytics.TempBytes = 1 << 20
	d.Analytics.Threads = 1
	d.Sites = []Site{{ID: "h2c", Enabled: true, Domains: []string{"cdn.test"}, Origins: []Origin{{ID: "one", URL: "http://127.0.0.1:9000", Protocol: "h2c"}}, DefaultOrigin: "one"}}
	if _, err := Validate(d); err != nil {
		t.Fatal(err)
	}
	d.Sites[0].Origins[0].URL = "https://example.test"
	if _, err := Validate(d); err == nil {
		t.Fatal("h2c accepted HTTPS origin")
	}
}
