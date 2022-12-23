// Copyright 2017 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/google/syzkaller/dashboard/dashapi"
	"github.com/google/syzkaller/pkg/hash"
	"golang.org/x/net/context"
	db "google.golang.org/appengine/v2/datastore"
)

// This file contains definitions of entities stored in datastore.

const (
	maxTextLen   = 200
	MaxStringLen = 1024

	maxBugHistoryDays = 365 * 5
)

type Manager struct {
	Namespace         string
	Name              string
	Link              string
	CurrentBuild      string
	FailedBuildBug    string
	FailedSyzBuildBug string
	LastAlive         time.Time
	CurrentUpTime     time.Duration
}

// ManagerStats holds per-day manager runtime stats.
// Has Manager as parent entity. Keyed by Date.
type ManagerStats struct {
	Date              int // YYYYMMDD
	MaxCorpus         int64
	MaxPCs            int64 // coverage
	MaxCover          int64 // what we call feedback signal everywhere else
	TotalFuzzingTime  time.Duration
	TotalCrashes      int64
	CrashTypes        int64 // unique crash types
	SuppressedCrashes int64
	TotalExecs        int64
}

type Asset struct {
	Type        dashapi.AssetType
	DownloadURL string
	CreateDate  time.Time
}

type Build struct {
	Namespace           string
	Manager             string
	ID                  string // unique ID generated by syz-ci
	Type                BuildType
	Time                time.Time
	OS                  string
	Arch                string
	VMArch              string
	SyzkallerCommit     string
	SyzkallerCommitDate time.Time
	CompilerID          string
	KernelRepo          string
	KernelBranch        string
	KernelCommit        string
	KernelCommitTitle   string    `datastore:",noindex"`
	KernelCommitDate    time.Time `datastore:",noindex"`
	KernelConfig        int64     // reference to KernelConfig text entity
	Assets              []Asset   // build-related assets
	AssetsLastCheck     time.Time // the last time we checked the assets for deprecation
}

type Bug struct {
	Namespace    string
	Seq          int64 // sequences of the bug with the same title
	Title        string
	MergedTitles []string // crash titles that we already merged into this bug
	AltTitles    []string // alternative crash titles that we may merge into this bug
	Status       int
	StatusReason dashapi.BugStatusReason // e.g. if the bug status is "invalid", here's the reason why
	DupOf        string
	NumCrashes   int64
	NumRepro     int64
	// ReproLevel is the best ever found repro level for this bug.
	// HeadReproLevel is best known repro level that still works on the HEAD commit.
	ReproLevel     dashapi.ReproLevel
	HeadReproLevel dashapi.ReproLevel `datastore:"HeadReproLevel"`
	BisectCause    BisectStatus
	BisectFix      BisectStatus
	HasReport      bool
	NeedCommitInfo bool
	FirstTime      time.Time
	LastTime       time.Time
	LastSavedCrash time.Time
	LastReproTime  time.Time
	FixTime        time.Time // when we become aware of the fixing commit
	LastActivity   time.Time // last time we observed any activity related to the bug
	Closed         time.Time
	Reporting      []BugReporting
	Commits        []string // titles of fixing commmits
	CommitInfo     []Commit // additional info for commits (for historical reasons parallel array to Commits)
	HappenedOn     []string // list of managers
	PatchedOn      []string `datastore:",noindex"` // list of managers
	UNCC           []string // don't CC these emails on this bug
	// Kcidb publishing status bitmask:
	// bit 0 - the bug is published
	// bit 1 - don't want to publish it (syzkaller build/test errors)
	KcidbStatus int64
	DailyStats  []BugDailyStats
	Tags        BugTags
}

type BugTags struct {
	Subsystems []BugSubsystem
}

type BugSubsystem struct {
	// For now, let's keep the bare minimum number of fields.
	// The subsystem names we use now are not stable and should not be relied upon.
	// Once the subsystem management functionality is fully implemented, we'll
	// override everything stored here.
	Name string
}

func (bug *Bug) addSubsystem(subsystem BugSubsystem) {
	for _, item := range bug.Tags.Subsystems {
		if item.Name == subsystem.Name {
			return
		}
	}
	bug.Tags.Subsystems = append(bug.Tags.Subsystems, subsystem)
}

func (bug *Bug) Load(ps []db.Property) error {
	if err := db.LoadStruct(bug, ps); err != nil {
		return err
	}
	headReproFound := false
	for _, p := range ps {
		if p.Name == "HeadReproLevel" {
			headReproFound = true
			break
		}
	}
	if !headReproFound {
		// The field is new, so it won't be set in all entities.
		// Assume it to be equal to the best found repro for the bug.
		bug.HeadReproLevel = bug.ReproLevel
	}
	return nil
}

func (bug *Bug) Save() ([]db.Property, error) {
	return db.SaveStruct(bug)
}

type BugDailyStats struct {
	Date       int // YYYYMMDD
	CrashCount int
}

type Commit struct {
	Hash       string
	Title      string
	Author     string
	AuthorName string
	CC         string `datastore:",noindex"` // (|-delimited list)
	Date       time.Time
}

type BugReporting struct {
	Name    string // refers to Reporting.Name
	ID      string // unique ID per BUG/BugReporting used in commucation with external systems
	ExtID   string // arbitrary reporting ID that is passed back in dashapi.BugReport
	Link    string
	CC      string // additional emails added to CC list (|-delimited list)
	CrashID int64  // crash that we've last reported in this reporting
	Auto    bool   // was it auto-upstreamed/obsoleted?
	// If Dummy is true, the corresponding Reporting stage was introduced later and the object was just
	// inserted to preserve consistency across the system. Even though it's indicated as Closed and Reported,
	// it never actually was.
	Dummy      bool
	ReproLevel dashapi.ReproLevel // may be less then bug.ReproLevel if repro arrived but we didn't report it yet
	OnHold     time.Time          // if set, the bug must not be upstreamed
	Reported   time.Time
	Closed     time.Time
}

type Crash struct {
	// May be different from bug.Title due to AltTitles.
	// May be empty for old bugs, in such case bug.Title is the right title.
	Title           string
	Manager         string
	BuildID         string
	Time            time.Time
	Reported        time.Time // set if this crash was ever reported
	References      []CrashReference
	Maintainers     []string            `datastore:",noindex"`
	Log             int64               // reference to CrashLog text entity
	Flags           int64               // properties of the Crash
	Report          int64               // reference to CrashReport text entity
	ReportElements  CrashReportElements // parsed parts of the crash report
	ReproOpts       []byte              `datastore:",noindex"`
	ReproSyz        int64               // reference to ReproSyz text entity
	ReproC          int64               // reference to ReproC text entity
	ReproIsRevoked  bool                // the repro no longer triggers the bug on HEAD
	LastReproRetest time.Time           // the last time when the repro was re-checked
	MachineInfo     int64               // Reference to MachineInfo text entity.
	// Custom crash priority for reporting (greater values are higher priority).
	// For example, a crash in mainline kernel has higher priority than a crash in a side branch.
	// For historical reasons this is called ReportLen.
	ReportLen       int64
	Assets          []Asset   // crash-related assets
	AssetsLastCheck time.Time // the last time we checked the assets for deprecation
}

type CrashReportElements struct {
	GuiltyFiles []string // guilty files as determined during the crash report parsing
}

type CrashReferenceType string

const (
	CrashReferenceReporting = "reporting"
	CrashReferenceJob       = "job"
	// This one is needed for backward compatibility.
	crashReferenceUnknown = "unknown"
)

type CrashReference struct {
	Type CrashReferenceType
	// For CrashReferenceReporting, it refers to Reporting.Name
	// For CrashReferenceJob, it refers to extJobID(jobKey)
	Key  string
	Time time.Time
}

func (crash *Crash) AddReference(newRef CrashReference) {
	crash.Reported = newRef.Time
	for i, ref := range crash.References {
		if ref.Type != newRef.Type || ref.Key != newRef.Key {
			continue
		}
		crash.References[i].Time = newRef.Time
		return
	}
	crash.References = append(crash.References, newRef)
}

func (crash *Crash) ClearReference(t CrashReferenceType, key string) {
	newRefs := []CrashReference{}
	crash.Reported = time.Time{}
	for _, ref := range crash.References {
		if ref.Type == t && ref.Key == key {
			continue
		}
		if ref.Time.After(crash.Reported) {
			crash.Reported = ref.Time
		}
		newRefs = append(newRefs, ref)
	}
	crash.References = newRefs
}

func (crash *Crash) Load(ps []db.Property) error {
	if err := db.LoadStruct(crash, ps); err != nil {
		return err
	}
	// Earlier we only relied on Reported, which does not let us reliably unreport a crash.
	// We need some means of ref counting, so let's create a dummy reference to keep the
	// crash from being purged.
	if !crash.Reported.IsZero() && len(crash.References) == 0 {
		crash.References = append(crash.References, CrashReference{
			Type: crashReferenceUnknown,
			Time: crash.Reported,
		})
	}
	return nil
}

func (crash *Crash) Save() ([]db.Property, error) {
	return db.SaveStruct(crash)
}

// ReportingState holds dynamic info associated with reporting.
type ReportingState struct {
	Entries []ReportingStateEntry
}

type ReportingStateEntry struct {
	Namespace string
	Name      string
	// Current reporting quota consumption.
	Sent int
	Date int // YYYYMMDD
}

// Job represent a single patch testing or bisection job for syz-ci.
// Later we may want to extend this to other types of jobs:
//   - test of a committed fix
//   - reproduce crash
//   - test that crash still happens on HEAD
//
// Job has Bug as parent entity.
type Job struct {
	Type      JobType
	Created   time.Time
	User      string
	CC        []string
	Reporting string
	ExtID     string // email Message-ID
	Link      string // web link for the job (e.g. email in the group)
	Namespace string
	Manager   string
	BugTitle  string
	CrashID   int64

	// Provided by user:
	KernelRepo   string
	KernelBranch string
	Patch        int64 // reference to Patch text entity
	KernelConfig int64 // reference to the kernel config entity

	Attempts    int       // number of times we tried to execute this job
	IsRunning   bool      // the job might have been started, but never finished
	LastStarted time.Time `datastore:"Started"`
	Finished    time.Time // if set, job is finished

	// Result of execution:
	CrashTitle  string // if empty, we did not hit crash during testing
	CrashLog    int64  // reference to CrashLog text entity
	CrashReport int64  // reference to CrashReport text entity
	Commits     []Commit
	BuildID     string
	Log         int64 // reference to Log text entity
	Error       int64 // reference to Error text entity, if set job failed
	Flags       JobFlags

	Reported bool // have we reported result back to user?
}

func (job *Job) IsFinished() bool {
	return !job.Finished.IsZero()
}

type JobType int

const (
	JobTestPatch JobType = iota
	JobBisectCause
	JobBisectFix
)

func (typ JobType) toDashapiReportType() dashapi.ReportType {
	switch typ {
	case JobTestPatch:
		return dashapi.ReportTestPatch
	case JobBisectCause:
		return dashapi.ReportBisectCause
	case JobBisectFix:
		return dashapi.ReportBisectFix
	default:
		panic(fmt.Sprintf("unknown job type %v", typ))
	}
}

type JobFlags int64

const (
	// Parallel to dashapi.JobDoneFlags, see comments there.
	BisectResultMerge JobFlags = 1 << iota
	BisectResultNoop
	BisectResultRelease
	BisectResultIgnore
)

func (flags JobFlags) String() string {
	res := ""
	if flags&BisectResultMerge != 0 {
		res += "merge "
	}
	if flags&BisectResultNoop != 0 {
		res += "no-op "
	}
	if flags&BisectResultRelease != 0 {
		res += "release "
	}
	if flags&BisectResultIgnore != 0 {
		res += "ignored "
	}
	if res == "" {
		return res
	}
	return "[" + res + "commit]"
}

func (job *Job) isUnreliableBisect() bool {
	if job.Type != JobBisectCause && job.Type != JobBisectFix {
		panic(fmt.Sprintf("bad job type %v", job.Type))
	}
	// If a bisection points to a merge or a commit that does not affect the kernel binary,
	// it is considered an unreliable/wrong result and should not be reported in emails.
	return job.Flags&BisectResultMerge != 0 ||
		job.Flags&BisectResultNoop != 0 ||
		job.Flags&BisectResultRelease != 0 ||
		job.Flags&BisectResultIgnore != 0
}

// Text holds text blobs (crash logs, reports, reproducers, etc).
type Text struct {
	Namespace string
	Text      []byte `datastore:",noindex"` // gzip-compressed text
}

const (
	textCrashLog     = "CrashLog"
	textCrashReport  = "CrashReport"
	textReproSyz     = "ReproSyz"
	textReproC       = "ReproC"
	textMachineInfo  = "MachineInfo"
	textKernelConfig = "KernelConfig"
	textPatch        = "Patch"
	textLog          = "Log"
	textError        = "Error"
)

const (
	BugStatusOpen = iota
)

const (
	BugStatusFixed = 1000 + iota
	BugStatusInvalid
	BugStatusDup
)

const (
	ReproLevelNone = dashapi.ReproLevelNone
	ReproLevelSyz  = dashapi.ReproLevelSyz
	ReproLevelC    = dashapi.ReproLevelC
)

type BuildType int

const (
	BuildNormal BuildType = iota
	BuildFailed
	BuildJob
)

type BisectStatus int

const (
	BisectNot BisectStatus = iota
	BisectPending
	BisectError
	BisectYes          // have 1 commit
	BisectUnreliable   // have 1 commit, but suspect it's wrong
	BisectInconclusive // multiple commits due to skips
	BisectHorizont     // happens on the oldest commit we can test (or HEAD for fix bisection)
	bisectStatusLast   // this value can be changed (not stored in datastore)
)

func (status BisectStatus) String() string {
	switch status {
	case BisectError:
		return "error"
	case BisectYes:
		return "done"
	case BisectUnreliable:
		return "unreliable"
	case BisectInconclusive:
		return "inconclusive"
	case BisectHorizont:
		return "inconclusive"
	default:
		return ""
	}
}

func mgrKey(c context.Context, ns, name string) *db.Key {
	return db.NewKey(c, "Manager", fmt.Sprintf("%v-%v", ns, name), 0, nil)
}

func (mgr *Manager) key(c context.Context) *db.Key {
	return mgrKey(c, mgr.Namespace, mgr.Name)
}

func loadManager(c context.Context, ns, name string) (*Manager, error) {
	mgr := new(Manager)
	if err := db.Get(c, mgrKey(c, ns, name), mgr); err != nil {
		if err != db.ErrNoSuchEntity {
			return nil, fmt.Errorf("failed to get manager %v/%v: %v", ns, name, err)
		}
		mgr = &Manager{
			Namespace: ns,
			Name:      name,
		}
	}
	return mgr, nil
}

// updateManager does transactional compare-and-swap on the manager and its current stats.
func updateManager(c context.Context, ns, name string, fn func(mgr *Manager, stats *ManagerStats) error) error {
	date := timeDate(timeNow(c))
	tx := func(c context.Context) error {
		mgr, err := loadManager(c, ns, name)
		if err != nil {
			return err
		}
		mgrKey := mgr.key(c)
		stats := new(ManagerStats)
		statsKey := db.NewKey(c, "ManagerStats", "", int64(date), mgrKey)
		if err := db.Get(c, statsKey, stats); err != nil {
			if err != db.ErrNoSuchEntity {
				return fmt.Errorf("failed to get stats %v/%v/%v: %v", ns, name, date, err)
			}
			stats = &ManagerStats{
				Date: date,
			}
		}

		if err := fn(mgr, stats); err != nil {
			return err
		}

		if _, err := db.Put(c, mgrKey, mgr); err != nil {
			return fmt.Errorf("failed to put manager: %v", err)
		}
		if _, err := db.Put(c, statsKey, stats); err != nil {
			return fmt.Errorf("failed to put manager stats: %v", err)
		}
		return nil
	}
	return db.RunInTransaction(c, tx, &db.TransactionOptions{Attempts: 10})
}

func loadAllManagers(c context.Context, ns string) ([]*Manager, []*db.Key, error) {
	var managers []*Manager
	query := db.NewQuery("Manager")
	if ns != "" {
		query = query.Filter("Namespace=", ns)
	}
	keys, err := query.GetAll(c, &managers)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query managers: %v", err)
	}
	var result []*Manager
	var resultKeys []*db.Key
	for i, mgr := range managers {
		if config.Namespaces[mgr.Namespace].Managers[mgr.Name].Decommissioned {
			continue
		}
		result = append(result, mgr)
		resultKeys = append(resultKeys, keys[i])
	}
	return result, resultKeys, nil
}

func buildKey(c context.Context, ns, id string) *db.Key {
	if ns == "" {
		panic("requesting build key outside of namespace")
	}
	h := hash.String([]byte(fmt.Sprintf("%v-%v", ns, id)))
	return db.NewKey(c, "Build", h, 0, nil)
}

func loadBuild(c context.Context, ns, id string) (*Build, error) {
	build := new(Build)
	if err := db.Get(c, buildKey(c, ns, id), build); err != nil {
		if err == db.ErrNoSuchEntity {
			return nil, fmt.Errorf("unknown build %v/%v", ns, id)
		}
		return nil, fmt.Errorf("failed to get build %v/%v: %v", ns, id, err)
	}
	return build, nil
}

func lastManagerBuild(c context.Context, ns, manager string) (*Build, error) {
	var builds []*Build
	_, err := db.NewQuery("Build").
		Filter("Namespace=", ns).
		Filter("Manager=", manager).
		Filter("Type=", BuildNormal).
		Order("-Time").
		Limit(1).
		GetAll(c, &builds)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch manager build: %v", err)
	}
	if len(builds) == 0 {
		return nil, fmt.Errorf("failed to fetch manager build: no builds")
	}
	return builds[0], nil
}

func (bug *Bug) displayTitle() string {
	if bug.Seq == 0 {
		return bug.Title
	}
	return fmt.Sprintf("%v (%v)", bug.Title, bug.Seq+1)
}

var displayTitleRe = regexp.MustCompile(`^(.*) \(([0-9]+)\)$`)

func splitDisplayTitle(display string) (string, int64, error) {
	match := displayTitleRe.FindStringSubmatchIndex(display)
	if match == nil {
		return display, 0, nil
	}
	title := display[match[2]:match[3]]
	seqStr := display[match[4]:match[5]]
	seq, err := strconv.ParseInt(seqStr, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("failed to parse bug title: %v", err)
	}
	if seq <= 0 || seq > 1e6 {
		return "", 0, fmt.Errorf("failed to parse bug title: seq=%v", seq)
	}
	return title, seq - 1, nil
}

func canonicalBug(c context.Context, bug *Bug) (*Bug, error) {
	for {
		if bug.Status != BugStatusDup {
			return bug, nil
		}
		canon := new(Bug)
		bugKey := db.NewKey(c, "Bug", bug.DupOf, 0, nil)
		if err := db.Get(c, bugKey, canon); err != nil {
			return nil, fmt.Errorf("failed to get dup bug %q for %q: %v",
				bug.DupOf, bug.keyHash(), err)
		}
		bug = canon
	}
}

func (bug *Bug) key(c context.Context) *db.Key {
	return db.NewKey(c, "Bug", bug.keyHash(), 0, nil)
}

func (bug *Bug) keyHash() string {
	return bugKeyHash(bug.Namespace, bug.Title, bug.Seq)
}

func bugKeyHash(ns, title string, seq int64) string {
	return hash.String([]byte(fmt.Sprintf("%v-%v-%v-%v", config.Namespaces[ns].Key, ns, title, seq)))
}

func loadSimilarBugs(c context.Context, bug *Bug) ([]*Bug, error) {
	domain := config.Namespaces[bug.Namespace].SimilarityDomain
	dedup := make(map[string]bool)
	dedup[bug.keyHash()] = true

	ret := []*Bug{}
	for _, title := range bug.AltTitles {
		var similar []*Bug
		_, err := db.NewQuery("Bug").
			Filter("AltTitles=", title).
			GetAll(c, &similar)
		if err != nil {
			return nil, err
		}
		for _, bug := range similar {
			if config.Namespaces[bug.Namespace].SimilarityDomain != domain ||
				dedup[bug.keyHash()] {
				continue
			}
			dedup[bug.keyHash()] = true
			ret = append(ret, bug)
		}
	}
	return ret, nil
}

// Since these IDs appear in Reported-by tags in commit, we slightly limit their size.
const reportingHashLen = 20

func bugReportingHash(bugHash, reporting string) string {
	return hash.String([]byte(fmt.Sprintf("%v-%v", bugHash, reporting)))[:reportingHashLen]
}

func looksLikeReportingHash(id string) bool {
	// This is only used as best-effort check.
	// Now we produce 20-chars ids, but we used to use full sha1 hash.
	return len(id) == reportingHashLen || len(id) == 2*len(hash.Sig{})
}

func (bug *Bug) updateCommits(commits []string, now time.Time) {
	bug.Commits = commits
	bug.CommitInfo = nil
	bug.NeedCommitInfo = true
	bug.FixTime = now
	bug.PatchedOn = nil
}

func (bug *Bug) getCommitInfo(i int) Commit {
	if i < len(bug.CommitInfo) {
		return bug.CommitInfo[i]
	}
	return Commit{}
}

func (bug *Bug) increaseCrashStats(now time.Time) {
	bug.NumCrashes++
	date := timeDate(now)
	if len(bug.DailyStats) == 0 || bug.DailyStats[len(bug.DailyStats)-1].Date < date {
		bug.DailyStats = append(bug.DailyStats, BugDailyStats{date, 1})
	} else {
		// It is theoretically possible that this method might get into a situation, when
		// the latest saved date is later than now. But we assume that this can only happen
		// in a small window around the start of the day and it is better to attribute a
		// crash to the next day than to get a mismatch between NumCrashes and the sum of
		// CrashCount.
		bug.DailyStats[len(bug.DailyStats)-1].CrashCount++
	}

	if len(bug.DailyStats) > maxBugHistoryDays {
		bug.DailyStats = bug.DailyStats[len(bug.DailyStats)-maxBugHistoryDays:]
	}
}

func (bug *Bug) dailyStatsTail(from time.Time) []BugDailyStats {
	startDate := timeDate(from)
	startPos := len(bug.DailyStats)
	for ; startPos > 0; startPos-- {
		if bug.DailyStats[startPos-1].Date < startDate {
			break
		}
	}
	return bug.DailyStats[startPos:]
}

func (bug *Bug) dashapiStatus() (dashapi.BugStatus, error) {
	var status dashapi.BugStatus
	switch bug.Status {
	case BugStatusOpen:
		status = dashapi.BugStatusOpen
	case BugStatusFixed:
		status = dashapi.BugStatusFixed
	case BugStatusInvalid:
		status = dashapi.BugStatusInvalid
	case BugStatusDup:
		status = dashapi.BugStatusDup
	default:
		return status, fmt.Errorf("unknown bugs status %v", bug.Status)
	}
	return status, nil
}

func addCrashReference(c context.Context, crashID int64, bugKey *db.Key, ref CrashReference) error {
	crash := new(Crash)
	crashKey := db.NewKey(c, "Crash", "", crashID, bugKey)
	if err := db.Get(c, crashKey, crash); err != nil {
		return fmt.Errorf("failed to get reported crash %v: %v", crashID, err)
	}
	crash.AddReference(ref)
	if _, err := db.Put(c, crashKey, crash); err != nil {
		return fmt.Errorf("failed to put reported crash %v: %v", crashID, err)
	}
	return nil
}

func removeCrashReference(c context.Context, crashID int64, bugKey *db.Key,
	t CrashReferenceType, key string) error {
	crash := new(Crash)
	crashKey := db.NewKey(c, "Crash", "", crashID, bugKey)
	if err := db.Get(c, crashKey, crash); err != nil {
		return fmt.Errorf("failed to get reported crash %v: %v", crashID, err)
	}
	crash.ClearReference(t, key)
	if _, err := db.Put(c, crashKey, crash); err != nil {
		return fmt.Errorf("failed to put reported crash %v: %v", crashID, err)
	}
	return nil
}

func kernelRepoInfo(build *Build) KernelRepo {
	return kernelRepoInfoRaw(build.Namespace, build.KernelRepo, build.KernelBranch)
}

func kernelRepoInfoRaw(ns, url, branch string) KernelRepo {
	var info KernelRepo
	for _, repo := range config.Namespaces[ns].Repos {
		if repo.URL == url && repo.Branch == branch {
			info = repo
			break
		}
	}
	if info.Alias == "" {
		info.Alias = url
		if branch != "" {
			info.Alias += " " + branch
		}
	}
	return info
}

func textLink(tag string, id int64) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("/text?tag=%v&x=%v", tag, strconv.FormatUint(uint64(id), 16))
}

// timeDate returns t's date as a single int YYYYMMDD.
func timeDate(t time.Time) int {
	year, month, day := t.Date()
	return year*10000 + int(month)*100 + day
}

func stringInList(list []string, str string) bool {
	for _, s := range list {
		if s == str {
			return true
		}
	}
	return false
}

func stringListsIntersect(a, b []string) bool {
	m := map[string]bool{}
	for _, strA := range a {
		m[strA] = true
	}
	for _, strB := range b {
		if m[strB] {
			return true
		}
	}
	return false
}

func mergeString(list []string, str string) []string {
	if !stringInList(list, str) {
		list = append(list, str)
	}
	return list
}

func mergeStringList(list, add []string) []string {
	for _, str := range add {
		list = mergeString(list, str)
	}
	return list
}

// dateTime converts date in YYYYMMDD format back to Time.
func dateTime(date int) time.Time {
	return time.Date(date/10000, time.Month(date/100%100), date%100, 0, 0, 0, 0, time.UTC)
}
