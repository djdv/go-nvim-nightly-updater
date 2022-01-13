package main

import (
	"archive/zip"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	bufra "github.com/avvmoto/buf-readerat"
	"github.com/google/go-github/v40/github"
	"github.com/snabb/httpreaderat"
)

func main() {
	log.SetFlags(log.Lshortfile)

	// TODO: runtime -> compile time (build constraint)
	var defaultAssetPlat,
		defaultAssetFormat string
	switch runtime.GOOS {
	case "windows":
		defaultAssetPlat = "win64"
		defaultAssetFormat = ".zip"
	case "darwin":
		defaultAssetPlat = "macos"
		defaultAssetFormat = ".tar.gz"
	case "linux":
		defaultAssetPlat = "linux64"
		defaultAssetFormat = ".tar.gz"
	}

	var (
		defaultPath      = filepath.Join("~", "path")
		defaultAssetName = "nvim-" + defaultAssetPlat + defaultAssetFormat

		repoOwner = flag.String("owner", "neovim", "Repository's owner")
		repoName  = flag.String("repo", "neovim", "Repository's name")
		tagName   = flag.String("tag", "nightly", "Release tag to look for")
		assetName = flag.String("release", defaultAssetName, "Release asset to fetch")
		target    = flag.String("path", defaultPath, "Path to install to")
	)
	flag.Parse()
	maybeExpandTilde(target)

	var (
		ctx    = context.Background()
		client = github.NewClient(nil)
	)

	repoRelease, _, err := client.Repositories.GetReleaseByTag(
		ctx, *repoOwner, *repoName, *tagName,
	)
	if err != nil {
		log.Fatal(err)
	}

	var releaseAsset *github.ReleaseAsset
	for _, release := range repoRelease.Assets {
		if release.Name == nil {
			continue
		}
		if *release.Name == *assetName {
			releaseAsset = release
			break
		}
	}
	if releaseAsset == nil {
		log.Fatalf("%q not found?", *assetName)
	}

	if releaseAsset.BrowserDownloadURL == nil {
		log.Fatalf("%q's url was nil", *assetName)
	}
	releaseAssetUrl := *releaseAsset.BrowserDownloadURL

	fmt.Println("grabbing: ", releaseAssetUrl)
	assetRequest, err := http.NewRequest(http.MethodGet, releaseAssetUrl, nil)
	if err != nil {
		log.Fatal(err)
	}

	assetHttpReader, err := httpreaderat.New(nil, assetRequest, nil)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: re-evaluate IO buffering. Does it actually make an impact for a straight shot here? Maybe zip jumps around the range, idk.
	bufSize := (1 << (10 * 2) * 2) // TODO: write this in English please; const sizeUnit, sizeCount, s = unit*count, etc.
	assetBufferReader := bufra.NewBufReaderAt(assetHttpReader, bufSize)

	assetZipReader, err := zip.NewReader(assetBufferReader, assetHttpReader.Size())
	if err != nil {
		log.Fatal(err)
	}

	if err := staggedExtraction(assetZipReader, *target); err != nil {
		log.Println(err)
	}

	if assetRequest.Close {
		if err := assetRequest.Response.Body.Close(); err != nil {
			log.Println(err)
		}
	}
}

func staggedExtraction(zipReader *zip.Reader, target string) error {
	// TODO: func stage(target) { move trg.ZipRoot.tmp; extract; if ok rm trg.ZipRoot.tmp; else undo}
	return extract(zipReader, target)
}

func extract(zipReader *zip.Reader, target string) error {
	// Closure to address file descriptors issue with all the deferred .Close() methods
	extractAndWriteFile := func(f *zip.File) error {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer func() {
			if err := rc.Close(); err != nil {
				panic(err)
			}
		}()

		path := filepath.Join(target, f.Name)

		// Check for ZipSlip (Directory traversal)
		if !strings.HasPrefix(path, filepath.Clean(target)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", path)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.Mode())
		} else {
			os.MkdirAll(filepath.Dir(path), f.Mode())
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return err
			}
			defer func() {
				if err := f.Close(); err != nil {
					panic(err)
				}
			}()

			_, err = io.Copy(f, rc)
			if err != nil {
				return err
			}
		}
		return nil
	}

	for _, f := range zipReader.File {
		fmt.Println("Extracting: ", f.Name) // TODO: -v only
		err := extractAndWriteFile(f)
		if err != nil {
			return err
		}
	}

	return nil
}

func maybeExpandTilde(path *string) {
	p := *path
	if !strings.HasPrefix(p, "~") {
		return
	}

	usr, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	*path = filepath.Join(usr.HomeDir, (p)[1:])
}
