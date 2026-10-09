//go:build ignore

// normalize-xpkg-base-layer converges the io.crossplane.xpkg:base layer of
// every per-platform .xpkg built by `make build.all` to one shared blob.
//
// `crossplane xpkg build` / `up xpkg build` re-serialize the provider's CRDs
// through gopkg.in/yaml.v2's Unmarshal/Marshal round trip, and that
// library's key comparator (keyList.Less, "natural sort": a run of digits
// compares as a number, so "interval2" sorts before "interval15") is not a
// strict total order over every key set -- e.g. across {interval1Day,
// interval1Hour, interval1Min, interval12Hours}, comparing digit-vs-letter
// at the first point of difference is not transitive. Go's sort.Sort on a
// non-transitive Less produces a result that depends on the *input*
// permutation, which for a map comes from Go's randomized map iteration
// order. Same CRD set, different process, different "sorted" bytes -- and
// the two platform builds (amd64, arm64) are two different processes, so
// they can disagree on the one layer ("package.yaml") whose content does
// not vary by architecture at all. The Upbound registry then rejects the
// index outright: "base layer content is inconsistent across images."
//
// This does not touch controller-gen, the CLI, or CRD field names. It runs
// AFTER `make build.all` has produced one .xpkg per platform and BEFORE
// they are pushed: it opens each platform's .xpkg (the legacy Docker-save
// tar format both `crossplane xpkg build` and `up xpkg build` emit -- a
// manifest.json plus one file per layer/config, each named by its own
// content hash), finds the layer whose entire content is the single file
// "package.yaml" -- that is the xpkg base layer, identified by structure
// because a pre-push local tar carries no OCI annotations to read instead
// -- and compares its uncompressed-content hash (its diffID) across every
// platform given.
//
// If every platform already agrees, nothing is touched: the exact input
// bytes are the exact output bytes, so an already-consistent package is
// byte-identical after this runs, not merely equivalent.
//
// If any platform disagrees, one platform is picked as the donor (the one
// whose own path sorts first, so the choice is reproducible from the same
// input every time) and every other platform's base layer is REPLACED with
// the donor's, verbatim -- same compressed bytes, so the decompressed
// package.yaml becomes byte-identical to the donor's. Before doing that,
// each replaced platform's package.yaml is decoded as a multi-document YAML
// stream and compared, document by document, against the donor's: this is
// a key-order bug precisely because the content never varies by platform,
// so if a real semantic difference turns up instead, that is not this bug
// and normalizing it away would silently destroy a real divergence. The
// tool refuses instead.
//
// Every other layer, and the rest of each platform's own config (os,
// architecture, History, and every RootFS.DiffIDs entry but the one that
// changed), is carried through unedited: the config file is patched by two
// literal-string substitutions, not by decoding and re-marshaling the whole
// structure, so nothing the CLI itself wrote can be reformatted, reordered,
// or dropped. The first swaps the old base layer's diffID (RootFS.DiffIDs,
// the uncompressed content hash) for the donor's. The second swaps the old
// base layer's own digest (the COMPRESSED content hash -- a layer's OCI
// identity, a different value from its diffID) inside the config's
// Labels["io.crossplane.xpkg:<digest>"] = "base" entry, which `crossplane
// xpkg build` / `up xpkg build` write for every annotated layer at build
// time. `up xpkg push`'s own annotate step (cmd/up/xpkg/push.go, the same
// logic as crossplane/internal/xpkg.AnnotateLayers) reads that Labels map
// at PUSH time, keyed by each layer's digest as computed from the actual
// pushed bytes, to decide which layer gets the io.crossplane.xpkg OCI
// annotation. Patching only the diffID and leaving this second, independent
// record pointing at a digest no layer in the rewritten image carries any
// more was exactly this tool's bug until it patched both: the base layer
// converged in content, but the pushed index carried the
// io.crossplane.xpkg=base annotation on the donor platform only, because
// push-time annotation lookup found nothing under the recipient's new
// (donor-identical) digest -- confirmed against a real pushed index with
// `check-xpkg-base-consistency.sh registry <ref>`, and against the exact
// annotate()/AnnotateLayers() source both `up` and `crossplane xpkg`
// share.
//
// Run from the provider module root, after `make build.all`:
//
//	go run hack/normalize-xpkg-base-layer.go _output/xpkg/linux_amd64/<pkg>.xpkg _output/xpkg/linux_arm64/<pkg>.xpkg
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"

	"gopkg.in/yaml.v3"
)

const baseLayerFile = "package.yaml"

// dockerManifest is the single entry every crossplane/up-built .xpkg's
// manifest.json carries. Field order matches what both CLIs emit, so a
// rewritten manifest.json marshals back the same shape.
type dockerManifest struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

// entry is one file inside the .xpkg tar, read fully into memory and kept
// in its original order so an unmodified platform can be rewritten with
// every byte the original had, in the original order, except the two or
// three files this tool explicitly changes.
type entry struct {
	header *tar.Header
	body   []byte
}

type platform struct {
	path     string
	entries  []entry
	byName   map[string]int // entry name -> index into entries
	manifest dockerManifest

	configName string
	configRaw  []byte

	baseLayerName   string
	baseLayerRaw    []byte // compressed, verbatim
	baseLayerDiffID string // "sha256:<hex>" of the UNCOMPRESSED content -- what
	// the config's RootFS.DiffIDs entry for this layer records.
	baseLayerDigest string // "sha256:<hex>" of the COMPRESSED content -- a
	// layer's own OCI digest, what the config's
	// Labels["io.crossplane.xpkg:<digest>"] entry keys on. Independent of
	// baseLayerDiffID; rewriteWithDonorBase must patch both.
	packageYAMLDocs []interface{}
}

func main() {
	paths := os.Args[1:]
	if len(paths) < 2 {
		fmt.Printf("SKIP: %d platform package(s) given; nothing to compare\n", len(paths))
		return
	}

	platforms := make([]*platform, 0, len(paths))
	for _, p := range paths {
		pf, err := loadPlatform(p)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", p, err))
		}
		platforms = append(platforms, pf)
	}

	// Deterministic donor selection: whichever path sorts first. Same input
	// paths, same donor, every run.
	sort.Slice(platforms, func(i, j int) bool { return platforms[i].path < platforms[j].path })
	donor := platforms[0]

	diverged := false
	for _, pf := range platforms[1:] {
		if pf.baseLayerDiffID != donor.baseLayerDiffID {
			diverged = true
			break
		}
	}
	if !diverged {
		fmt.Printf("xpkg base layer already consistent across %d platform(s) (%s)\n", len(platforms), donor.baseLayerDiffID)
		return
	}

	for _, pf := range platforms[1:] {
		if pf.baseLayerDiffID == donor.baseLayerDiffID {
			continue
		}
		if err := verifySemanticEquivalence(donor, pf); err != nil {
			fatal(fmt.Errorf("%s: %w", pf.path, err))
		}
		if err := rewriteWithDonorBase(pf, donor); err != nil {
			fatal(fmt.Errorf("%s: %w", pf.path, err))
		}
		fmt.Printf("%s: base layer %s -> %s (from donor %s)\n", pf.path, pf.baseLayerDiffID, donor.baseLayerDiffID, donor.path)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "normalize-xpkg-base-layer: FATAL:", err)
	os.Exit(1)
}

// loadPlatform reads an entire .xpkg tar into memory, preserving entry
// order, and identifies its base layer and config file by structure.
func loadPlatform(path string) (*platform, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	pf := &platform{path: path, byName: map[string]int{}}

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", hdr.Name, err)
		}
		pf.byName[hdr.Name] = len(pf.entries)
		pf.entries = append(pf.entries, entry{header: hdr, body: body})
	}

	mi, ok := pf.byName["manifest.json"]
	if !ok {
		return nil, fmt.Errorf("no manifest.json in tar")
	}
	var manifests []dockerManifest
	if err := json.Unmarshal(pf.entries[mi].body, &manifests); err != nil {
		return nil, fmt.Errorf("parsing manifest.json: %w", err)
	}
	if len(manifests) != 1 {
		return nil, fmt.Errorf("manifest.json has %d image entries, want exactly 1", len(manifests))
	}
	pf.manifest = manifests[0]

	ci, ok := pf.byName[pf.manifest.Config]
	if !ok {
		return nil, fmt.Errorf("manifest.json Config %q has no matching file in tar", pf.manifest.Config)
	}
	pf.configName = pf.manifest.Config
	pf.configRaw = pf.entries[ci].body

	for _, layerName := range pf.manifest.Layers {
		li, ok := pf.byName[layerName]
		if !ok {
			return nil, fmt.Errorf("manifest.json layer %q has no matching file in tar", layerName)
		}
		layerBody := pf.entries[li].body
		files, uncompressed, err := layerContents(layerBody)
		if err != nil {
			return nil, fmt.Errorf("layer %s: %w", layerName, err)
		}
		if len(files) == 1 {
			if name, content := files[0].name, files[0].body; name == baseLayerFile {
				diffSum := sha256.Sum256(uncompressed)
				digestSum := sha256.Sum256(layerBody)
				pf.baseLayerName = layerName
				pf.baseLayerRaw = layerBody
				pf.baseLayerDiffID = "sha256:" + hex.EncodeToString(diffSum[:])
				pf.baseLayerDigest = "sha256:" + hex.EncodeToString(digestSum[:])
				docs, err := decodeYAMLDocs(content)
				if err != nil {
					return nil, fmt.Errorf("decoding %s: %w", baseLayerFile, err)
				}
				pf.packageYAMLDocs = docs
			}
		}
	}
	if pf.baseLayerName == "" {
		return nil, fmt.Errorf("no layer whose sole content is %s -- cannot identify the xpkg base layer", baseLayerFile)
	}
	return pf, nil
}

type layerFile struct {
	name string
	body []byte
}

// layerContents gunzips a layer blob and returns every file its tar holds
// (name plus content), plus the full uncompressed byte stream -- the latter
// is what a layer's diffID hashes, per the OCI/Docker image spec: the
// digest of the uncompressed tar stream itself, not of any file inside it.
func layerContents(compressed []byte) ([]layerFile, []byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, nil, fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()
	uncompressed, err := io.ReadAll(gz)
	if err != nil {
		return nil, nil, fmt.Errorf("gunzip: %w", err)
	}

	var files []layerFile
	tr := tar.NewReader(bytes.NewReader(uncompressed))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading layer tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			// A non-regular entry (dir, symlink, ...) means this layer is
			// not "a single package.yaml file", regardless of Name.
			files = append(files, layerFile{name: hdr.Name + "/"})
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s from layer tar: %w", hdr.Name, err)
		}
		files = append(files, layerFile{name: hdr.Name, body: body})
	}
	return files, uncompressed, nil
}

func decodeYAMLDocs(raw []byte) ([]interface{}, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var docs []interface{}
	for {
		var v interface{}
		if err := dec.Decode(&v); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		docs = append(docs, v)
	}
	return docs, nil
}

// verifySemanticEquivalence proves the divergence is exactly the key-order
// bug -- same documents, different bytes -- and not a real content
// difference the CLI happens to have made between platforms. Reusing the
// donor's layer for anything but the key-order bug would silently destroy
// that difference, so this refuses rather than guesses.
func verifySemanticEquivalence(donor, pf *platform) error {
	if len(donor.packageYAMLDocs) != len(pf.packageYAMLDocs) {
		return fmt.Errorf("%s document count %d differs from donor %s document count %d -- refusing to overwrite: this is a real content divergence, not the key-order bug",
			baseLayerFile, len(pf.packageYAMLDocs), donor.path, len(donor.packageYAMLDocs))
	}
	for i := range donor.packageYAMLDocs {
		if !reflect.DeepEqual(donor.packageYAMLDocs[i], pf.packageYAMLDocs[i]) {
			return fmt.Errorf("%s document %d differs semantically from donor %s -- refusing to overwrite: this is a real content divergence, not the key-order bug",
				baseLayerFile, i, donor.path)
		}
	}
	return nil
}

// rewriteWithDonorBase replaces pf's base layer with donor's, patches pf's
// own config to point its RootFS diffID entry AND its annotation Labels key
// at the donor's values (two independent literal-string substitutions, so
// every other byte of the config -- including History -- survives
// untouched), and writes the result back to pf.path atomically. Every entry
// that is neither the base layer, the config, nor manifest.json is copied
// through verbatim: same name, same header, same bytes.
func rewriteWithDonorBase(pf, donor *platform) error {
	oldDiffIDQuoted := []byte(`"` + pf.baseLayerDiffID + `"`)
	newDiffIDQuoted := []byte(`"` + donor.baseLayerDiffID + `"`)
	if n := bytes.Count(pf.configRaw, oldDiffIDQuoted); n != 1 {
		return fmt.Errorf("config %s contains %d occurrence(s) of %s, want exactly 1 -- refusing to guess which one is the base layer's",
			pf.configName, n, oldDiffIDQuoted)
	}
	newConfigRaw := bytes.Replace(pf.configRaw, oldDiffIDQuoted, newDiffIDQuoted, 1)

	// The Labels map entry recording this layer's io.crossplane.xpkg
	// annotation is keyed by the layer's OWN digest (the compressed content
	// hash), a record entirely independent of the diffID patched above --
	// see this file's header comment. Swapping in the donor's base layer
	// bytes changes pf's base layer digest to the donor's, so the Labels
	// key must follow or `up xpkg push` finds no entry for the new digest
	// and silently annotates nothing for this platform.
	oldLabelKeyQuoted := []byte(`"io.crossplane.xpkg:` + pf.baseLayerDigest + `"`)
	newLabelKeyQuoted := []byte(`"io.crossplane.xpkg:` + donor.baseLayerDigest + `"`)
	if n := bytes.Count(newConfigRaw, oldLabelKeyQuoted); n != 1 {
		return fmt.Errorf("config %s contains %d occurrence(s) of the base layer's annotation label key %s, want exactly 1 -- refusing to guess which one is the base layer's",
			pf.configName, n, oldLabelKeyQuoted)
	}
	newConfigRaw = bytes.Replace(newConfigRaw, oldLabelKeyQuoted, newLabelKeyQuoted, 1)

	newConfigSum := sha256.Sum256(newConfigRaw)
	newConfigName := "sha256:" + hex.EncodeToString(newConfigSum[:])

	newManifest := pf.manifest
	newManifest.Config = newConfigName
	newLayers := make([]string, len(pf.manifest.Layers))
	copy(newLayers, pf.manifest.Layers)
	for i, name := range newLayers {
		if name == pf.baseLayerName {
			newLayers[i] = donor.baseLayerName
		}
	}
	newManifest.Layers = newLayers
	newManifestRaw, err := json.Marshal([]dockerManifest{newManifest})
	if err != nil {
		return fmt.Errorf("marshaling manifest.json: %w", err)
	}

	tmp := pf.path + ".normalize-tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(out)

	writeErr := func() error {
		for _, e := range pf.entries {
			switch e.header.Name {
			case "manifest.json":
				if err := writeEntry(tw, e.header, newManifestRaw); err != nil {
					return err
				}
			case pf.configName:
				if err := writeEntry(tw, e.header, newConfigRaw, newConfigName); err != nil {
					return err
				}
			case pf.baseLayerName:
				if err := writeEntry(tw, e.header, donor.baseLayerRaw, donor.baseLayerName); err != nil {
					return err
				}
			default:
				if err := writeEntry(tw, e.header, e.body); err != nil {
					return err
				}
			}
		}
		return nil
	}()
	closeErr := tw.Close()
	syncErr := out.Close()
	if writeErr != nil {
		os.Remove(tmp)
		return writeErr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}
	if syncErr != nil {
		os.Remove(tmp)
		return syncErr
	}
	return os.Rename(tmp, pf.path)
}

// writeEntry writes body under hdr's original metadata (mode/uid/gid/mtime),
// with an optionally-renamed Name and the new Size, into tw.
func writeEntry(tw *tar.Writer, hdr *tar.Header, body []byte, newName ...string) error {
	out := *hdr
	if len(newName) > 0 {
		out.Name = newName[0]
	}
	out.Size = int64(len(body))
	if err := tw.WriteHeader(&out); err != nil {
		return fmt.Errorf("writing header for %s: %w", out.Name, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("writing body for %s: %w", out.Name, err)
	}
	return nil
}
