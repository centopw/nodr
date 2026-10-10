package sync

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// RendererID identifies the proxmoxvm/provision/opentofu lens for the
// renderer field of .nodr/render.lock. It is independent of the nodr build
// version and of the bpg/proxmox provider version.
const RendererID = "proxmoxvm@1"

// Block is one entry of .nodr/render.lock: the mapping between a managed
// resource and the exact block of code that renders it (design §4.6).
type Block struct {
	UID          string `yaml:"uid"`
	Ref          string `yaml:"ref"`
	Aspect       string `yaml:"aspect"`
	Engine       string `yaml:"engine"`
	File         string `yaml:"file"`
	Address      string `yaml:"address"`
	Renderer     string `yaml:"renderer"`
	PristineHash string `yaml:"pristineHash,omitempty"`
}

// Lock is the decoded content of .nodr/render.lock.
type Lock struct {
	Blocks []Block `yaml:"blocks"`
}

// ParseLock decodes the content of a .nodr/render.lock file.
func ParseLock(src []byte) (Lock, error) {
	var l Lock
	if len(src) == 0 {
		return l, nil
	}
	if err := yaml.Unmarshal(src, &l); err != nil {
		return Lock{}, fmt.Errorf("parse render.lock: %w", err)
	}
	return l, nil
}

// Marshal encodes the lock back to YAML.
func (l Lock) Marshal() ([]byte, error) {
	out, err := yaml.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("marshal render.lock: %w", err)
	}
	return out, nil
}

// ByUID returns the block for the given resource UID.
func (l Lock) ByUID(uid string) (Block, bool) {
	for _, b := range l.Blocks {
		if b.UID == uid {
			return b, true
		}
	}
	return Block{}, false
}

// WithBlock returns a copy of l with block set: replacing the existing
// entry for block.UID if there is one, or appending it otherwise. Blocks
// keep their relative order, so unrelated diffs in the lock file stay
// minimal.
func (l Lock) WithBlock(block Block) Lock {
	out := Lock{Blocks: make([]Block, len(l.Blocks))}
	copy(out.Blocks, l.Blocks)
	for i, b := range out.Blocks {
		if b.UID == block.UID {
			out.Blocks[i] = block
			return out
		}
	}
	out.Blocks = append(out.Blocks, block)
	return out
}
