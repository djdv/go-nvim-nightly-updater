package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	bufra "github.com/avvmoto/buf-readerat"
	"github.com/google/go-github/v40/github"
	"github.com/snabb/httpreaderat"
)

func main() {
	log.SetFlags(log.Lshortfile)
	const (
		repoOwner = "neovim"
		repoName  = "neovim"
		tagName   = "nightly"
	)
	var (
		ctx    = context.Background()
		client = github.NewClient(nil)
	)

	repoRelease, _, err := client.Repositories.GetReleaseByTag(
		ctx, repoOwner, repoName, tagName,
	)
	if err != nil {
		log.Fatal(err)
	}

	const windowsReleaseName = "nvim-win64.zip"
	var windowsRelease *github.ReleaseAsset
	for _, release := range repoRelease.Assets {
		if release.Name == nil {
			continue
		}
		if *release.Name == windowsReleaseName {
			windowsRelease = release
			break
		}
	}
	if windowsRelease == nil {
		log.Fatalf("%q not found?", windowsReleaseName)
	}

	if windowsRelease.BrowserDownloadURL == nil {
		log.Fatalf("%q's url was nil", windowsReleaseName)
	}
	releaseAssetUrl := *windowsRelease.BrowserDownloadURL

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

	// TODO: get target from args
	usr, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	target := filepath.Join(usr.HomeDir, "path")

	if err := staggedExtraction(assetZipReader, target); err != nil {
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
