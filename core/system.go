package core

import (
	"errors"
	"os"

	"github.com/google/uuid"
	"github.com/vanilla-os/abroot/settings"
)

/*	License: GPLv3
	Authors:
		Mirko Brombin <mirko@fabricators.ltd>
		Vanilla OS Contributors <https://github.com/vanilla-os/>
	Copyright: 2023
	Description:
		ABRoot is utility which provides full immutability and
		atomicity to a Linux system, by transacting between
		two root filesystems. Updates are performed using OCI
		images, to ensure that the system is always in a
		consistent state.
*/

// ABSystem represents the system
type ABSystem struct {
	Checks   *Checks
	RootM    *ABRootManager
	Registry *Registry
	CurImage *ABImage
}

type QueuedFunction struct {
	Name   string
	Values []interface{}
}

var queue []QueuedFunction

// NewABSystem creates a new system
func NewABSystem() (*ABSystem, error) {
	PrintVerbose("NewABSystem: running...")

	i, err := NewABImageFromRoot()
	if err != nil {
		PrintVerbose("NewABSystem:error: %s", err)
		return nil, err
	}

	c := NewChecks()
	r := NewRegistry()
	rm := NewABRootManager()

	return &ABSystem{
		Checks:   c,
		RootM:    rm,
		Registry: r,
		CurImage: i,
	}, nil
}

// CheckAll performs all checks from the Checks struct
func (s *ABSystem) CheckAll() error {
	PrintVerbose("ABSystem.CheckAll: running...")

	err := s.Checks.PerformAllChecks()
	if err != nil {
		PrintVerbose("ABSystem.CheckAll:error: %s", err)
		return err
	}

	PrintVerbose("ABSystem.CheckAll: all checks passed")
	return nil
}

// CheckUpdate checks if there is an update available
func (s *ABSystem) CheckUpdate() bool {
	PrintVerbose("ABSystem.CheckUpdate: running...")
	return s.Registry.HasUpdate(s.CurImage.Digest)
}

// SyncEtc syncs /var/lib/abroot/etc -> .system/etc
func (s *ABSystem) SyncEtc(systemEtc string) error {
	PrintVerbose("ABSystem.SyncEtc: syncing /var/lib/abroot/etc -> %s", systemEtc)

	etcFiles := []string{
		"passwd",
		"group",
		"shells",
		"shadow",
		"subuid",
		"subgid",
	}

	for _, file := range etcFiles {
		sourceFile := "/var/lib/abroot/etc/" + file
		destFile := systemEtc + "/" + file

		// write the diff to the destination
		err := MergeDiff(sourceFile, destFile)
		if err != nil {
			PrintVerbose("ABSystem.SyncEtc:error: %s", err)
			return err
		}
	}

	PrintVerbose("ABSystem.SyncEtc: sync completed")
	return nil
}

// RunCleanUpQueue runs the functions in the queue
func (s *ABSystem) RunCleanUpQueue() error {
	PrintVerbose("ABSystem.RunCleanUpQueue: running...")

	for _, f := range queue {
		switch f.Name {
		case "umountFuture":
			futurePart := f.Values[0].(ABRootPartition)
			err := futurePart.Partition.Unmount()
			if err != nil {
				PrintVerbose("ABSystem.RunCleanUpQueue:error: %s", err)
				return err
			}
		case "closeChroot":
			chroot := f.Values[0].(*Chroot)
			err := chroot.Close()
			if err != nil {
				PrintVerbose("ABSystem.RunCleanUpQueue:error(2): %s", err)
				return err
			}
		}
	}

	s.ResetQueue()

	PrintVerbose("ABSystem.RunCleanUpQueue: completed")
	return nil
}

// AddToCleanUpQueue adds a function to the queue
func (s *ABSystem) AddToCleanUpQueue(name string, values ...interface{}) {
	queue = append(queue, QueuedFunction{
		Name:   name,
		Values: values,
	})
}

// ResetQueue resets the queue
func (s *ABSystem) ResetQueue() {
	queue = []QueuedFunction{}
}

// Upgrade upgrades the system to the latest available image
func (s *ABSystem) Upgrade() error {
	PrintVerbose("ABSystem.Upgrade: starting upgrade")

	s.ResetQueue()

	// Are hooks supposed to exist in ABRoot v2?
	// hooksM := NewHooks()
	// hooksFinalPre, err := hooksM.FinalScript("pre")
	// if err != nil {
	// 	PrintVerbose("ABSystem.Upgrade:error: %s", err)
	// 	return err
	// }
	// hooksFinalPost, err := hooksM.FinalScript("post")
	// if err != nil {
	// 	PrintVerbose("ABSystem.Upgrade:error: %s", err)
	// 	return err
	// }

	// Stage 0: Check if there is an update available
	PrintVerbose("[Stage 0] ABSystemUpgrade")

	if !s.CheckUpdate() {
		err := errors.New("no update available")
		PrintVerbose("ABSystemUpgrade:error: %s", err)
		return err
	}

	// Stage 1: Get the future root and boot partitions
	// 			and mount future to /part-future
	PrintVerbose("[Stage 1] ABSystemUpgrade")

	partFuture, err := s.RootM.GetFuture()
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(2): %s", err)
		return err
	}

	partBoot, err := s.RootM.GetBoot()
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(3): %s", err)
		return err
	}

	err = partFuture.Partition.Mount("/part-future/")
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(4): %s", err)
		return err
	}

	s.AddToCleanUpQueue("umountFuture", partFuture)

	// Stage 2: Pull the new image
	PrintVerbose("[Stage 2] ABSystemUpgrade")

	podman := NewPodman()
	fullImageName := settings.Cnf.Registry + "/" + settings.Cnf.Name + ":" + settings.Cnf.Tag
	podmanImage, err := podman.Pull(fullImageName)
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(5): %s", err)
		return err
	}

	// Stage 3: Make a Containerfile with user packages
	PrintVerbose("[Stage 3] ABSystemUpgrade")

	pkgM := NewPackageManager()
	pkgsFinal := pkgM.GetFinalCmd()

	labels := map[string]string{
		"maintainer": "'Generated by ABRoot'",
	}
	args := map[string]string{}
	if pkgsFinal == "" {
		pkgsFinal = "true"
	}
	content := `RUN ` + pkgsFinal

	containerFile := podman.NewContainerFile(
		fullImageName,
		labels,
		args,
		content,
	)

	// Stage 4: Extract the rootfs
	PrintVerbose("[Stage 4] ABSystemUpgrade")

	err = podman.GenerateRootfs(
		fullImageName,
		containerFile,
		partFuture.Partition.MountPoint,
		partFuture.Partition.MountPoint+"/.system.new/",
	)
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(6): %s", err)
		return err
	}

	// Stage 5: Write abimage.abr.new to future/
	PrintVerbose("[Stage 5] ABSystemUpgrade")

	abimage := NewABImage(
		podmanImage.Digest,
		fullImageName,
	)
	err = abimage.WriteTo(partFuture.Partition.MountPoint, "new")
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(7): %s", err)
		return err
	}

	// Stage 6: Atomic swap the rootfs and abimage.abr
	PrintVerbose("[Stage 6] ABSystemUpgrade")

	err = AtomicSwap(
		partFuture.Partition.MountPoint+"/.system/",
		partFuture.Partition.MountPoint+"/.system.new/",
	)
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(8): %s", err)
		return err
	}

	oldABImage := partFuture.Partition.MountPoint + "/abimage.abr"
	newABImage := partFuture.Partition.MountPoint + "/abimage-new.abr"
	err = AtomicSwap(oldABImage, newABImage)
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(9): %s", err)
		return err
	}

	// Stage 7: Update the bootloader
	PrintVerbose("[Stage 7] ABSystemUpgrade")

	err = generateGrubRecipe(
		partFuture.Partition.MountPoint+"/.system/",
		partFuture.Partition.Uuid,
		partFuture.IdentifiedAs,
	)
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(10): %s", err)
		return err
	}

	chroot, err := NewChroot(
		partFuture.Partition.MountPoint+"/.system/",
		partFuture.Partition.Uuid,
		partFuture.Partition.Device,
	)
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(10.1): %s", err)
		return err
	}

	s.AddToCleanUpQueue("closeChroot", chroot)

	err = chroot.ExecuteCmds(
		[]string{
			"grub-mkconfig -o /boot/grub/grub.cfg",
			"exit",
		},
	)
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(11): %s", err)
		return err
	}

	// Stage 8: Sync /etc
	PrintVerbose("[Stage 8] ABSystemUpgrade")

	err = s.SyncEtc(partFuture.Partition.MountPoint + "/.system/etc/")
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(12): %s", err)
		return err
	}

	// Stage 9: Mount boot partition
	PrintVerbose("[Stage 9] ABSystemUpgrade")

	uuid := uuid.New().String()
	err = os.Mkdir("/tmp/"+uuid, 0755)
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(13): %s", err)
		return err
	}

	err = partBoot.Mount("/tmp/" + uuid)
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(14): %s", err)
		return err
	}

	// Stage 10: Atomic swap the bootloader
	PrintVerbose("[Stage 10] ABSystemUpgrade")

	err = AtomicSwap(
		"/tmp/"+uuid+"/grub.cfg",
		"/tmp/"+uuid+"/grub.cfg.future",
	)
	if err != nil {
		PrintVerbose("ABSystem.Upgrade:error(15): %s", err)
		return err
	}

	PrintVerbose("ABSystem.Upgrade: upgrade completed")
	return nil
}
