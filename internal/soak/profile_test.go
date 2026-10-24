//go:build soakscale

package soak

import "fmt"

// Profile is the dataset size. M is the locked PRD target profile; S is 2% of M
// (generator smoke); H is the home profile used only for comparison.
type Profile struct {
	Name              string
	Owners            int // subscribed owners
	OneShotOwners     int
	Sources           int
	HeavyOwners       int
	HeavyReleases     int // per heavy owner
	TypicalReleases   int // per typical owner (average)
	Acquisitions      int // accepted downloads (graphs)
	WorkAvailable     int
	WorkTerminal      int // retained terminal
	WorkExpired       int // terminal, expired, waiting for the janitor
	LogEntries        int
	LogExpired        int
	PluginDeliveries  int
	PluginAttempts    int
	DispatchRuns      int
	Routines          int
	Episodes          int
	CatalogSnapshots  int // provider_catalog_snapshots
	CatalogEntriesPer int
	CatalogExpired    int
	GroupSnapshots    int
	FeedSnapshots     int
	FileManagerOps    int
	ReconObs          int
	PlacementDirs     int
	RealSample        int // acquisitions accepted through the real accept path
}

func profileByName(name string) (Profile, error) {
	switch name {
	case "S":
		return scaleProfile("S", 0.02), nil
	case "H":
		p := scaleProfile("H", 0.10)
		p.Owners = 50
		p.OneShotOwners = 2
		p.Sources = 80
		p.HeavyOwners = 1
		p.HeavyReleases = 2500
		p.TypicalReleases = 51
		return p, nil
	case "M", "":
		return profileM(), nil
	}
	return Profile{}, fmt.Errorf("unknown SOAK_PROFILE %q (S, M or H)", name)
}

func profileM() Profile {
	return Profile{
		Name: "M", Owners: 500, OneShotOwners: 20, Sources: 800,
		HeavyOwners: 10, HeavyReleases: 2500, TypicalReleases: 60,
		Acquisitions: 15000, WorkAvailable: 20000, WorkTerminal: 150000, WorkExpired: 30000,
		LogEntries: 250000, LogExpired: 50000,
		PluginDeliveries: 20000, PluginAttempts: 25000,
		DispatchRuns: 5000, Routines: 20000, Episodes: 500,
		CatalogSnapshots: 240, CatalogEntriesPer: 300, CatalogExpired: 100,
		GroupSnapshots: 2000, FeedSnapshots: 3000,
		FileManagerOps: 5000, ReconObs: 20000,
		PlacementDirs: 400, RealSample: 50,
	}
}

// scaleProfile is M multiplied by f (every count at least a small floor so the
// shapes still exist).
func scaleProfile(name string, f float64) Profile {
	m := profileM()
	s := func(v, floor int) int {
		x := int(float64(v) * f)
		if x < floor {
			return floor
		}
		return x
	}
	return Profile{
		Name: name, Owners: s(m.Owners, 10), OneShotOwners: s(m.OneShotOwners, 2), Sources: s(m.Sources, 16),
		HeavyOwners: s(m.HeavyOwners, 1), HeavyReleases: s(m.HeavyReleases, 100), TypicalReleases: m.TypicalReleases,
		Acquisitions: s(m.Acquisitions, 60), WorkAvailable: s(m.WorkAvailable, 100), WorkTerminal: s(m.WorkTerminal, 200),
		WorkExpired: s(m.WorkExpired, 100), LogEntries: s(m.LogEntries, 500), LogExpired: s(m.LogExpired, 100),
		PluginDeliveries: s(m.PluginDeliveries, 60), PluginAttempts: s(m.PluginAttempts, 80),
		DispatchRuns: s(m.DispatchRuns, 20), Routines: s(m.Routines, 50), Episodes: s(m.Episodes, 5),
		CatalogSnapshots: s(m.CatalogSnapshots, 8), CatalogEntriesPer: s(m.CatalogEntriesPer, 20), CatalogExpired: s(m.CatalogExpired, 4),
		GroupSnapshots: s(m.GroupSnapshots, 10), FeedSnapshots: s(m.FeedSnapshots, 10),
		FileManagerOps: s(m.FileManagerOps, 20), ReconObs: s(m.ReconObs, 50),
		PlacementDirs: s(m.PlacementDirs, 20), RealSample: 10,
	}
}
