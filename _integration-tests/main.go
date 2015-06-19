package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	baseDir         = "/tmp/snappy-test"
	debsTestBedPath = "/tmp/snappy-debs"
	defaultRelease  = "15.04"
	defaultChannel  = "edge"
	defaultArch     = "amd64"
)

var (
	defaultDebsDir   = filepath.Join(baseDir, "debs")
	imageDir         = filepath.Join(baseDir, "image")
	outputDir        = filepath.Join(baseDir, "output")
	imageTarget      = filepath.Join(imageDir, "snappy.img")
	commonSSHOptions = []string{
		"ssh", "-s", "/usr/share/autopkgtest/ssh-setup/snappy"}
	kvmSSHOptions = append(commonSSHOptions, []string{"--", "-i", imageTarget}...)
	debsDir       string
	arch          string
	testbedIP     string
)

func init() {
	flag.StringVar(&debsDir, "debs-dir", defaultDebsDir, "Directory with the snappy debian packages.")
	flag.StringVar(&arch, "arch", defaultArch, "Target architecture (amd64, armhf)")
	flag.StringVar(&testbedIP, "ip", "", "IP of the testbed to run the tests in")
}

func execCommand(cmds ...string) {
	cmd := exec.Command(cmds[0], cmds[1:len(cmds)]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Error while running %s: %s\n", cmd.Args, err)
	}
}

func buildDebs(rootPath, arch string) {
	fmt.Println("Building debs...")
	prepareTargetDir(debsDir)
	buildCommand := []string{"bzr", "bd",
		fmt.Sprintf("--result-dir=%s", debsDir),
		"--split",
		rootPath,
	}
	if arch != defaultArch {
		buildCommand = append(buildCommand, "--", "--host", arch)
	} else {
		dontSignDebs := []string{"--", "-uc", "-us"}
		buildCommand = append(buildCommand, dontSignDebs...)
	}
	fmt.Println(buildCommand)
	execCommand(buildCommand...)
}

func createImage(release, channel, arch string) {
	fmt.Println("Creating image...")
	prepareTargetDir(imageDir)
	execCommand(
		"sudo", "ubuntu-device-flash", "--verbose",
		"core", release,
		"-o", imageTarget,
		fmt.Sprintf("--oem=%s", arch),
		"--channel", channel,
		"--developer-mode")
}

func adtRun(rootPath string, sshOptions []string) {
	fmt.Println("Calling adt-run...")
	prepareTargetDir(outputDir)
	cmd := []string{"adt-run",
		"-B",
		"--setup-commands", "touch /run/autopkgtest_no_reboot.stamp",
		"--setup-commands", "mount -o remount,rw /",
		"--setup-commands",
		fmt.Sprintf("dpkg -i %s/*deb", debsTestBedPath),
		"--setup-commands",
		"sync; sleep 2; mount -o remount,ro /",
		"--override-control", "debian/integration-tests/control",
		"--built-tree", rootPath,
		"--output-dir", outputDir,
		fmt.Sprintf("--copy=%s:%s", debsDir, debsTestBedPath),
		"---"}
	execCommand(append(cmd, sshOptions...)...)
}

func remoteTestbedSSHOptions(testbedIP string) []string {
	options := []string{
		"-H", testbedIP,
	}
	return append(commonSSHOptions, options...)
}

func prepareTargetDir(targetDir string) {
	if _, err := os.Stat(targetDir); err == nil {
		// dir exists, remove it
		os.RemoveAll(targetDir)
	}
	os.MkdirAll(targetDir, 0777)
}

func getRootPath() string {
	dir, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	return dir
}

func getArchForImage() string {
	return fmt.Sprintf("generic-%s", defaultArch)
}

func main() {
	flag.Parse()

	rootPath := getRootPath()

	if debsDir == defaultDebsDir {
		buildDebs(rootPath, arch)
	}
	if arch == defaultArch {
		createImage(defaultRelease, defaultChannel, getArchForImage())
		adtRun(rootPath, kvmSSHOptions)
	} else {
		//adtRun(rootPath, remoteTestbedSSHOptions(testbedIP))
	}
}
