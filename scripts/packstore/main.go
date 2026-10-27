// packstore 打包 CPA 插件商店的归档文件与校验和。
//
// 用 Go 实现而不是 shell：`zip` / `sha256sum` 在 Windows runner 上不可用，
// 而这里必须三平台行为一致。
//
// 宿主 internal/pluginstore 的安装契约：
//
//   - 归档资产名：{id}_{version}_{goos}_{goarch}.zip
//     （version 是 tag 去掉前导 v，例如 tag v0.1.0 → 0.1.0）
//   - 校验和资产名必须**恰好**是 checksums.txt，格式为 `<sha256>  <文件名>`
//   - zip 内**根目录**必须有且仅有一个动态库，文件名必须是 {id}{ext}
//     或 {id}-v{version}{ext}；zip 内出现其它动态库会被拒绝
//   - 宿主会校验 zip 的 sha256，然后解包到 {pluginsDir}/{goos}/{goarch}/
//
// 两种模式：
//
//	打包：    packstore -lib dist/today-linux-amd64.so -id today \
//	                   -version 0.1.0 -goos linux -goarch amd64 -out-dir dist
//	校验和：  packstore -checksums-dir dist      # 汇总目录下所有 *.zip → checksums.txt
//
// 汇总步骤必须单独做：矩阵里每个平台各生成一份 checksums.txt 会互相覆盖，
// 导致部分 zip 的校验和丢失、安装时直接失败。
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if errRun := run(os.Args[1:], os.Stdout); errRun != nil {
		fmt.Fprintf(os.Stderr, "packstore: %v\n", errRun)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("packstore", flag.ContinueOnError)
	lib := fs.String("lib", "", "编译产物路径（打包模式）")
	id := fs.String("id", "", "插件 ID，必须与 pluginID 一致（打包模式）")
	version := fs.String("version", "", "版本号，tag 去掉前导 v（打包模式）")
	goos := fs.String("goos", "", "目标 GOOS（打包模式）")
	goarch := fs.String("goarch", "", "目标 GOARCH（打包模式）")
	outDir := fs.String("out-dir", "dist", "归档输出目录（打包模式）")
	checksumsDir := fs.String("checksums-dir", "", "汇总 checksums.txt 的目录（校验和模式）")

	if errParse := fs.Parse(args); errParse != nil {
		return errParse
	}

	if *checksumsDir != "" {
		return writeChecksums(*checksumsDir, stdout)
	}
	if *lib == "" || *id == "" || *version == "" || *goos == "" || *goarch == "" {
		return fmt.Errorf("bundle mode requires -lib, -id, -version, -goos and -goarch")
	}
	return bundle(bundleSpec{
		Lib:     *lib,
		ID:      *id,
		Version: *version,
		GOOS:    *goos,
		GOARCH:  *goarch,
		OutDir:  *outDir,
	}, stdout)
}

type bundleSpec struct {
	Lib     string
	ID      string
	Version string
	GOOS    string
	GOARCH  string
	OutDir  string
}

// libraryName 返回归档内动态库应使用的文件名：{id}{ext}。
func libraryName(id, goos string) (string, error) {
	switch goos {
	case "windows":
		return id + ".dll", nil
	case "darwin":
		return id + ".dylib", nil
	case "linux":
		return id + ".so", nil
	}
	return "", fmt.Errorf("unsupported goos %q", goos)
}

func bundle(spec bundleSpec, stdout io.Writer) error {
	libName, errName := libraryName(spec.ID, spec.GOOS)
	if errName != nil {
		return errName
	}
	src, errOpen := os.Open(spec.Lib)
	if errOpen != nil {
		return fmt.Errorf("open library: %w", errOpen)
	}
	defer src.Close()

	info, errStat := src.Stat()
	if errStat != nil {
		return fmt.Errorf("stat library: %w", errStat)
	}
	if info.IsDir() {
		return fmt.Errorf("library path is a directory: %s", spec.Lib)
	}

	if errMkdir := os.MkdirAll(spec.OutDir, 0o755); errMkdir != nil {
		return fmt.Errorf("create output dir: %w", errMkdir)
	}
	archiveName := fmt.Sprintf("%s_%s_%s_%s.zip", spec.ID, spec.Version, spec.GOOS, spec.GOARCH)
	archivePath := filepath.Join(spec.OutDir, archiveName)

	dst, errCreate := os.Create(archivePath)
	if errCreate != nil {
		return fmt.Errorf("create archive: %w", errCreate)
	}
	zw := zip.NewWriter(dst)

	entry, errEntry := zw.CreateHeader(&zip.FileHeader{
		Name:   libName,
		Method: zip.Deflate,
	})
	if errEntry != nil {
		dst.Close()
		return fmt.Errorf("create zip entry: %w", errEntry)
	}
	if _, errCopy := io.Copy(entry, src); errCopy != nil {
		dst.Close()
		return fmt.Errorf("write zip entry: %w", errCopy)
	}
	if errClose := zw.Close(); errClose != nil {
		dst.Close()
		return fmt.Errorf("close zip: %w", errClose)
	}
	if errClose := dst.Close(); errClose != nil {
		return fmt.Errorf("close archive: %w", errClose)
	}

	digest, errDigest := fileSHA256(archivePath)
	if errDigest != nil {
		return errDigest
	}
	fmt.Fprintf(stdout, "%s  %s\n", digest, archiveName)
	return nil
}

// writeChecksums 汇总目录下全部 zip，生成唯一的 checksums.txt。
func writeChecksums(dir string, stdout io.Writer) error {
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		return fmt.Errorf("read checksums dir: %w", errRead)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".zip") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return fmt.Errorf("no .zip archives found in %s", dir)
	}
	sort.Strings(names)

	var sb strings.Builder
	for _, name := range names {
		digest, errDigest := fileSHA256(filepath.Join(dir, name))
		if errDigest != nil {
			return errDigest
		}
		sb.WriteString(digest)
		sb.WriteString("  ")
		sb.WriteString(name)
		sb.WriteString("\n")
	}
	target := filepath.Join(dir, "checksums.txt")
	if errWrite := os.WriteFile(target, []byte(sb.String()), 0o644); errWrite != nil {
		return fmt.Errorf("write checksums: %w", errWrite)
	}
	fmt.Fprintf(stdout, "wrote %s (%d archives)\n", target, len(names))
	return nil
}

func fileSHA256(path string) (string, error) {
	f, errOpen := os.Open(path)
	if errOpen != nil {
		return "", fmt.Errorf("open %s: %w", path, errOpen)
	}
	defer f.Close()
	h := sha256.New()
	if _, errCopy := io.Copy(h, f); errCopy != nil {
		return "", fmt.Errorf("hash %s: %w", path, errCopy)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
