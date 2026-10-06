package app

import "testing"

func TestPCSpecsSeedDefaults(t *testing.T) {
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for pass := 0; pass < 2; pass++ {
		if err := s.Seed(); err != nil {
			t.Fatal(err)
		}
		for _, want := range []struct {
			ids   []int64
			specs string
		}{
			{[]int64{1, 2, 3, 4}, "CPU: Intel Core i5-12400F · RAM: 16 GB · GPU: RTX 4060"},
			{[]int64{5, 6, 7, 8}, "CPU: Intel Core i7-12700F · RAM: 32 GB · GPU: RTX 4070"},
			{[]int64{9, 10, 11, 12}, "CPU: Intel Core i9-13900F · RAM: 32 GB · GPU: RTX 4080"},
		} {
			for _, id := range want.ids {
				var got string
				if err := s.db.QueryRow("SELECT specs FROM pcs WHERE id=?", id).Scan(&got); err != nil || got != want.specs {
					t.Fatalf("Seed pass %d, PC %d specs = %q, want %q, error=%v", pass, id, got, want.specs, err)
				}
			}
		}
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM pcs").Scan(&count); err != nil || count != 12 {
			t.Fatalf("Seeding must retain exactly 12 default stations: %d, %v", count, err)
		}
	}
}

func TestPCSpecsLegacyUpgradePreservesEdits(t *testing.T) {
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`
UPDATE pcs SET specs='RTX 4060 · 16 GB' WHERE id BETWEEN 1 AND 4;
UPDATE pcs SET specs='RTX 4070 · 32 GB' WHERE id BETWEEN 5 AND 8;
UPDATE pcs SET specs='RTX 4080 · 32 GB' WHERE id BETWEEN 9 AND 12;
UPDATE pcs SET code='CUSTOM-02' WHERE id=2;
UPDATE pcs SET tier_id=2 WHERE id=3;
UPDATE pcs SET specs='CPU: Ryzen 7 · RAM: 64 GB · GPU: Radeon' WHERE id=4;
UPDATE pcs SET enabled=0 WHERE id=5;
UPDATE tiers SET price_pesos='39.50' WHERE id=1;
INSERT INTO pcs(id,code,tier_id,specs,enabled) VALUES(13,'PC-13',1,'RTX 4060 · 16 GB',1);`)
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		if err := s.Seed(); err != nil {
			t.Fatal(err)
		}
		for _, want := range []struct {
			id    int64
			specs string
		}{
			{1, "CPU: Intel Core i5-12400F · RAM: 16 GB · GPU: RTX 4060"},
			{2, "RTX 4060 · 16 GB"},
			{3, "RTX 4060 · 16 GB"},
			{4, "CPU: Ryzen 7 · RAM: 64 GB · GPU: Radeon"},
			{5, "CPU: Intel Core i7-12700F · RAM: 32 GB · GPU: RTX 4070"},
			{9, "CPU: Intel Core i9-13900F · RAM: 32 GB · GPU: RTX 4080"},
			{13, "RTX 4060 · 16 GB"},
		} {
			var got string
			if err := s.db.QueryRow("SELECT specs FROM pcs WHERE id=?", want.id).Scan(&got); err != nil || got != want.specs {
				t.Fatalf("Seed pass %d, PC %d specs = %q, want %q, error=%v", pass, want.id, got, want.specs, err)
			}
		}
		var code, price string
		var tierID int64
		var enabled bool
		if err := s.db.QueryRow("SELECT code FROM pcs WHERE id=2").Scan(&code); err != nil || code != "CUSTOM-02" {
			t.Fatalf("Custom station code changed: %q, %v", code, err)
		}
		if err := s.db.QueryRow("SELECT tier_id FROM pcs WHERE id=3").Scan(&tierID); err != nil || tierID != 2 {
			t.Fatalf("Custom station tier changed: %d, %v", tierID, err)
		}
		if err := s.db.QueryRow("SELECT enabled FROM pcs WHERE id=5").Scan(&enabled); err != nil || enabled {
			t.Fatalf("Disabled station became enabled: %v, %v", enabled, err)
		}
		if err := s.db.QueryRow("SELECT price_pesos FROM tiers WHERE id=1").Scan(&price); err != nil || price != "39.50" {
			t.Fatalf("Edited rate changed: %q, %v", price, err)
		}
	}
}
