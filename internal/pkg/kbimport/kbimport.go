// Package kbimport 提供知识库增量入库共用的文件扫描、内容指纹与增量判定逻辑。
// 供 Worker 的 kb:scan 周期任务复用，避免扫描/读取逻辑在多个入口重复实现而漂移。
package kbimport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxFileSize 限制单个源文件最大读取体积（与前端上传一致，10MB）。
const MaxFileSize = 10 << 20

// textExtensions 为可入库的文本类文件后缀白名单。
// 说明：PDF 等二进制格式不在扫描范围内（需先经离线工具抽取为文本）。
var textExtensions = map[string]struct{}{
	".md": {}, ".markdown": {}, ".txt": {}, ".text": {}, ".rst": {},
	".json": {}, ".yaml": {}, ".yml": {}, ".csv": {}, ".tsv": {},
	".go": {}, ".py": {}, ".js": {}, ".ts": {}, ".java": {}, ".c": {},
	".cpp": {}, ".h": {}, ".hpp": {}, ".rs": {}, ".sql": {}, ".sh": {},
	".html": {}, ".htm": {}, ".xml": {},
}

// File 表示一次扫描得到的源文件。
type File struct {
	Path    string // 绝对路径，作为文档的 source_path 唯一标识来源
	Content string // UTF-8 文本内容（已去 BOM）
	Hash    string // 内容 sha256（hex），增量去重与变更检测的判据
}

// Action 表示某个源文件相对库中已有文档应当执行的动作。
type Action int

const (
	// ActionCreate：库中不存在该来源 → 新建文档并入队构建。
	ActionCreate Action = iota
	// ActionSkip：库中已存在且内容指纹相同 → 跳过（零成本）。
	ActionSkip
	// ActionRebuild：库中已存在但内容指纹变化 → 先失效清理再重建。
	ActionRebuild
)

// String 便于日志与测试输出。
func (a Action) String() string {
	switch a {
	case ActionCreate:
		return "create"
	case ActionSkip:
		return "skip"
	case ActionRebuild:
		return "rebuild"
	default:
		return "unknown"
	}
}

// HashContent 计算文本内容的 sha256（十六进制小写）。
func HashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Decide 依据库中已有指纹与扫描到的新指纹决定增量动作。
// existingHash 为空表示库中没有该来源文档（新建）。
func Decide(existingHash, newHash string) Action {
	switch {
	case existingHash == "":
		return ActionCreate
	case existingHash == newHash:
		return ActionSkip
	default:
		return ActionRebuild
	}
}

// ScanDir 递归扫描单个目录下的文本文件，最多返回 limit 个文件（limit<=0 表示不限制）。
// 跳过隐藏目录/隐藏文件、不支持的扩展名、空文件与超过 MaxFileSize 的文件；
// 单个文件读取失败不阻断整批扫描，按绝对路径排序返回，保证多次扫描顺序稳定。
func ScanDir(root string, limit int) ([]File, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("kbimport: stat source %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("kbimport: source %s is not a directory", root)
	}

	files := make([]File, 0)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !isTextFile(name) {
			return nil
		}
		if limit > 0 && len(files) >= limit {
			return fs.SkipAll
		}
		f, rerr := readFile(path)
		if rerr != nil {
			// 单个文件不可用时跳过，不阻断整批扫描。
			return nil
		}
		files = append(files, f)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("kbimport: walk %s: %w", root, walkErr)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func isTextFile(name string) bool {
	_, ok := textExtensions[strings.ToLower(filepath.Ext(name))]
	return ok
}

func readFile(path string) (File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return File{}, err
	}
	if info.Size() == 0 || info.Size() > MaxFileSize {
		return File{}, fmt.Errorf("kbimport: skip %s (size %d)", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	content := strings.TrimPrefix(string(data), "\ufeff") // 去除 UTF-8 BOM
	if strings.TrimSpace(content) == "" {
		return File{}, fmt.Errorf("kbimport: skip empty %s", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return File{Path: abs, Content: content, Hash: HashContent(content)}, nil
}
