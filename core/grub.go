package core

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
)

// generateGrubRecipe generates a new grub recipe with the given details
// kernel version is automatically detected
func generateGrubRecipe(rootPath string, rootUuid string, entryName string) error {
	PrintVerbose("generateGrubConfig: generating grub config")

	recipePath := filepath.Join(rootPath, "etc", "grub.d", "10_abroot")
	// following template is based on vanilla os 2.0, needs to be updated
	// to support other distros (and remove what's not needed)
	template := `#!/bin/sh
# ABRoot GRUB configuration file
# This file is automatically generated by ABRoot
# Do not edit this file manually

exec tail -n +3 $0

set menu_color_normal=white/black
set menu_color_highlight=black/light-gray

function gfxmode {
	set gfxpayload="${1}"
	if [ "${1}" = "keep" ]; then
					set vt_handoff=vt.handoff=7
	else
					set vt_handoff=
	fi
}
if [ "${recordfail}" != 1 ]; then
if [ -e ${prefix}/gfxblacklist.txt ]; then
if [ ${grub_platform} != pc ]; then
  set linux_gfx_mode=keep
elif hwmatch ${prefix}/gfxblacklist.txt 3; then
  if [ ${match} = 0 ]; then
	set linux_gfx_mode=keep
  else
	set linux_gfx_mode=text
  fi
else
  set linux_gfx_mode=text
fi
else
set linux_gfx_mode=keep
fi
else
set linux_gfx_mode=text
fi
export linux_gfx_mode

menuentry '%s' --class gnu-linux --class gnu --class os {
	recordfail
	load_video
	gfxmode $linux_gfx_mode
	insmod gzio
	if [ x$grub_platform = xxen ]; then insmod xzio; insmod lzopio; fi
	insmod part_gpt
	insmod ext2
	search --no-floppy --fs-uuid --set=root %s
	linux   /.system/boot/vmlinuz-%s root=UUID=%s quiet splash bgrt_disable $vt_handoff
	initrd  /.system/boot/initrd.img-%s`

	kernelVersion := getKernelVersion(rootPath)
	if kernelVersion == "" {
		err := errors.New("could not get kernel version")
		PrintVerbose("generateGrubConfig:err: %s", err)
		return err
	}

	grubDir := filepath.Join(rootPath, "etc", "grub.d")
	err := os.MkdirAll(grubDir, 0755)
	if err != nil {
		PrintVerbose("generateGrubConfig:err(2): %s", err)
		return err
	}

	err = ioutil.WriteFile(
		recipePath,
		[]byte(fmt.Sprintf(template, entryName, rootUuid, kernelVersion, rootUuid, kernelVersion)),
		0644,
	)
	if err != nil {
		PrintVerbose("generateGrubConfig:err(3): %s", err)
		return err
	}

	return nil
}

// getKernelVersion returns the latest kernel version available in the root
func getKernelVersion(rootPath string) string {
	PrintVerbose("getKernelVersion: getting kernel version")

	kernelDir := filepath.Join(rootPath, "boot", "vmlinuz-*")
	files, err := filepath.Glob(kernelDir)
	if err != nil {
		PrintVerbose("getKernelVersion:err: %s", err)
		return ""
	}

	if len(files) == 0 {
		PrintVerbose("getKernelVersion:err: no kernel found")
		return ""
	}

	var maxVersion string
	for _, file := range files {
		version := filepath.Base(file)
		if version > maxVersion {
			maxVersion = version
		}
	}

	return maxVersion
}
