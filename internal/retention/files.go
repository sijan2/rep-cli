package retention

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"golang.org/x/sys/unix"
)

func gcInventory(dir string) (map[string]os.FileInfo, error) {
	entries := map[string]os.FileInfo{}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("task store must be a real directory")
	}
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == dir {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		entries[filepath.ToSlash(relative)] = info
		return nil
	})
	return entries, err
}

func gcSame(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime() == after.ModTime() && before.Mode() == after.Mode()
}

func gcOpenLocked(path string, exclusive bool) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("not a readable regular file")
	}
	lock := unix.LOCK_SH
	if exclusive {
		lock = unix.LOCK_EX
	}
	if err := unix.Flock(descriptor, lock|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func gcUnlock(files []*os.File) {
	for _, file := range files {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}
}

// Live exporters use atomic replacement, while session appenders lock the log
// inode. Check writable descriptors too, including appenders awaiting that lock,
// so replacement never intentionally strands a known writer on the old inode.
func gcHasWriter(path string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "lsof", "-nP", "-Fpa", "--", path)
	data, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && len(exit.Stderr) == 0 && len(data) == 0 {
			return false, nil
		}
		return false, fmt.Errorf("lsof could not establish writer state")
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "aw" || line == "au" {
			return true, nil
		}
	}
	return false, nil
}

func gcSessionStaged(entries map[string]os.FileInfo) bool {
	for name := range entries {
		if strings.HasPrefix(name, ".rep-session-") {
			return true
		}
	}
	return false
}

func gcReferenceName(name string) bool {
	base := filepath.Base(name)
	for _, prefix := range []string{"notes", "note", "findings", "finding", "probes", "probe", "identities"} {
		if name == prefix || name == prefix+".json" || name == prefix+".jsonl" || name == prefix+".lock" || name == prefix+".jsonl.lock" || name == prefix+".json.lock" || strings.HasPrefix(name, prefix+"/") {
			return true
		}
	}
	return strings.HasPrefix(name, "evidence/") && (strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".jsonl") || base == ".lock")
}

func gcReferenceStamps(entries map[string]os.FileInfo) map[string]os.FileInfo {
	stamps := map[string]os.FileInfo{}
	for name, info := range entries {
		if gcReferenceName(name) || name == "store.json" || name == "evidence" || strings.HasPrefix(name, "evidence/") || name == "notes" || name == "findings" || name == "finding" || name == "probes" || name == "probe" {
			stamps[name] = info
		}
	}
	return stamps
}

func gcReferences(dir string, entries map[string]os.FileInfo) (map[string]string, []*os.File, map[string]os.FileInfo, error) {
	refs := map[string]string{}
	stamps := gcReferenceStamps(entries)
	for name, info := range stamps {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, nil, fmt.Errorf("cannot establish references through symlink %s", name)
		}
	}
	var names []string
	for name := range entries {
		if gcReferenceName(name) {
			names = append(names, name)
		}
	}
	// Journal directory locks must precede their metadata files. Evidence
	// writers use these locks while publishing operations and references.
	sort.Slice(names, func(i, j int) bool {
		ilock, jlock := strings.HasSuffix(names[i], ".lock"), strings.HasSuffix(names[j], ".lock")
		if ilock != jlock {
			return ilock
		}
		return names[i] < names[j]
	})
	var locks []*os.File
	fail := func(err error) (map[string]string, []*os.File, map[string]os.FileInfo, error) {
		gcUnlock(locks)
		return nil, nil, nil, err
	}
	for _, name := range names {
		info := entries[name]
		if info.IsDir() {
			continue
		}
		if !info.Mode().IsRegular() {
			return fail(fmt.Errorf("cannot establish evidence references through %s", name))
		}
		if !strings.HasSuffix(name, ".lock") && !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		file, err := gcOpenLocked(filepath.Join(dir, name), false)
		if err != nil {
			return fail(fmt.Errorf("reference file %s is unavailable or being written: %w", name, err))
		}
		locks = append(locks, file)
		if strings.HasSuffix(name, ".lock") {
			continue
		}
		reader := gcNewReader(file)
		for {
			err := reader.gcSpace()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fail(err)
			}
			raw, err := reader.gcRaw()
			if err != nil {
				return fail(fmt.Errorf("cannot establish references in %s: %w", name, err))
			}
			var value any
			if err := sonic.Unmarshal(raw, &value); err != nil {
				return fail(err)
			}
			gcCollectRefs(value, name, refs)
		}
		current, err := file.Stat()
		if err != nil || !gcSame(info, current) {
			return fail(fmt.Errorf("reference file %s changed during scan", name))
		}
	}
	return refs, locks, stamps, nil
}

func gcCollectRefs(value any, source string, refs map[string]string) {
	switch value := value.(type) {
	case string:
		if value != "" {
			if _, exists := refs[value]; !exists {
				refs[value] = source
			}
		}
		for _, token := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '\n' || r == '\r' || strings.ContainsRune("[]{}()\"'`,;=#", r)
		}) {
			token = strings.Trim(token, ".:")
			if token != "" {
				if _, exists := refs[token]; !exists {
					refs[token] = source
				}
			}
			for _, part := range strings.Split(token, "/") {
				if strings.HasPrefix(part, "h_") || strings.HasPrefix(part, "s_") {
					refs[part] = source
				}
			}
		}
	case map[string]any:
		var keys []string
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			gcCollectRefs(key, source, refs)
			gcCollectRefs(value[key], source, refs)
		}
	case []any:
		for _, item := range value {
			gcCollectRefs(item, source, refs)
		}
	}
}

func gcCheckReferences(dir string, before map[string]os.FileInfo) error {
	entries, err := gcInventory(dir)
	if err != nil {
		return err
	}
	after := gcReferenceStamps(entries)
	if len(before) != len(after) {
		return fmt.Errorf("evidence references or primary settings changed; run gc plan again")
	}
	for name, info := range before {
		if !gcSame(info, after[name]) {
			return fmt.Errorf("evidence references or primary settings changed at %s; run gc plan again", name)
		}
	}
	return nil
}

func RetentionApply(plan *RetentionPlan) (*RetentionReport, error) {
	if plan == nil || plan.Report == nil {
		return nil, fmt.Errorf("a retention plan is required")
	}
	if plan.options.Status {
		return nil, fmt.Errorf("status is not an eviction plan")
	}
	// Copy the public projection so callers can compare the approved snapshot
	// with actual results, including files skipped after a concurrent change.
	raw, err := sonic.Marshal(plan.Report)
	if err != nil {
		return nil, err
	}
	var report RetentionReport
	if err := sonic.Unmarshal(raw, &report); err != nil {
		return nil, err
	}
	report.RemainingBytes = report.TotalBytes
	entries, err := gcInventory(plan.dir)
	if err != nil {
		return &report, err
	}
	_, locks, _, err := gcReferences(plan.dir, entries)
	if err != nil {
		return &report, err
	}
	defer gcUnlock(locks)
	if err := gcCheckReferences(plan.dir, plan.references); err != nil {
		return &report, err
	}
	for _, file := range plan.files {
		var selected []*gcRecord
		for _, record := range file.records {
			if record.reason != "" {
				selected = append(selected, record)
			}
		}
		if len(selected) == 0 {
			continue
		}
		if file.skipped != "" {
			report.gcSkip(file.name, file.skipped, file.stamp.Size())
			continue
		}
		if file.name == "sessions.jsonl" && gcSessionStaged(entries) {
			report.gcSkip(file.name, "session_append_in_progress", file.stamp.Size())
			continue
		}
		if err := gcCheckReferences(plan.dir, plan.references); err != nil {
			return &report, err
		}
		reclaimed, skipped, err := gcRewrite(plan, file, selected)
		if skipped != "" {
			report.gcSkip(file.name, skipped, file.stamp.Size())
			continue
		}
		if err != nil {
			return &report, err
		}
		report.ReclaimedBytes += reclaimed
		report.RemainingBytes -= reclaimed
		for _, record := range selected {
			usage := report.Classes[record.class]
			usage.EvictedRecords++
			usage.EvictedBodyBytes += record.bodyBytes
			savings := record.ageSavings
			if record.reason == "retention_budget" {
				savings = record.budgetSavings
			}
			usage.ReclaimedBytes += savings
		}
		// store.json is also a protection source. Accept only this invocation's
		// intentional body transformation when validating the remaining files.
		if file.name == "store.json" {
			info, err := os.Lstat(filepath.Join(plan.dir, file.name))
			if err != nil {
				return &report, err
			}
			plan.references[file.name] = info
		}
	}
	report.Applied = true
	report.BudgetUnmetBytes = max(0, report.RemainingBytes-plan.options.MaxBytes)
	return &report, nil
}

func gcRewrite(plan *RetentionPlan, source *gcFile, selected []*gcRecord) (int64, string, error) {
	path := filepath.Join(plan.dir, source.name)
	file, err := gcOpenLocked(path, true)
	if err != nil {
		return 0, "currently_being_written_or_unavailable", nil
	}
	defer gcUnlock([]*os.File{file})
	info, err := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !gcSame(source.stamp, info) || !gcSame(source.stamp, pathInfo) {
		return 0, "changed_since_plan", nil
	}
	writing, err := gcHasWriter(path)
	if err != nil {
		return 0, "writer_check_unavailable", nil
	}
	if writing {
		return 0, "currently_being_written", nil
	}
	temp, err := os.CreateTemp(plan.dir, ".rep-gc-*.tmp")
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = temp.Close(); _ = os.Remove(temp.Name()) }()
	if err := temp.Chmod(0600); err != nil {
		return 0, "", err
	}
	writer := bufio.NewWriterSize(temp, 64<<10)
	var offset int64
	for _, record := range selected {
		if record.start < offset || record.end < record.start {
			return 0, "", fmt.Errorf("invalid eviction offsets")
		}
		if _, err := io.Copy(writer, io.NewSectionReader(file, offset, record.start-offset)); err != nil {
			return 0, "", err
		}
		raw, err := io.ReadAll(io.NewSectionReader(file, record.start, record.end-record.start))
		if err != nil {
			return 0, "", err
		}
		updated, err := gcEvict(raw, plan.options.Now, record.class, record.reason)
		if err != nil {
			return 0, "", err
		}
		if _, err := writer.Write(updated); err != nil {
			return 0, "", err
		}
		offset = record.end
	}
	if _, err := io.Copy(writer, io.NewSectionReader(file, offset, source.stamp.Size()-offset)); err != nil {
		return 0, "", err
	}
	if err := writer.Flush(); err != nil {
		return 0, "", err
	}
	if err := temp.Sync(); err != nil {
		return 0, "", err
	}
	updatedInfo, err := temp.Stat()
	if err != nil {
		return 0, "", err
	}
	if err := temp.Close(); err != nil {
		return 0, "", err
	}
	current, err := file.Stat()
	pathInfo, pathErr = os.Lstat(path)
	if err != nil || pathErr != nil || !gcSame(source.stamp, current) || !gcSame(source.stamp, pathInfo) {
		return 0, "changed_during_rewrite", nil
	}
	if err := gcCheckReferences(plan.dir, plan.references); err != nil {
		return 0, "", err
	}
	entries, err := gcInventory(plan.dir)
	if err != nil {
		return 0, "", err
	}
	if source.name == "sessions.jsonl" && gcSessionStaged(entries) {
		return 0, "session_append_in_progress", nil
	}
	writing, err = gcHasWriter(path)
	if err != nil {
		return 0, "writer_check_unavailable", nil
	}
	if writing {
		return 0, "currently_being_written", nil
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return 0, "", err
	}
	if directory, err := os.Open(plan.dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return source.stamp.Size() - updatedInfo.Size(), "", nil
}
