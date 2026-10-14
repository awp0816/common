// Package snowflake 雪花ID：起始时间基点(Epoch)持久化到文件，服务重启后基点不变，生成的ID依然单调递增(不处理系统时钟回拨)
//
//	if err := snowflake.Init(1, "data/snowflake"); err != nil { ... }
//	id := snowflake.GenInt64() // 数据库主键
//	s := snowflake.GenID()     // 字符串形式
package snowflake

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bwmarrin/snowflake"
	"github.com/pkg/errors"
)

// Config 雪花ID配置
type Config struct {
	// Node 节点号 0~1023，多实例部署时每个实例不同
	Node int64 `json:"node" yaml:"node" mapstructure:"node"`
	// EpochFile 起始时间基点文件，默认 snowflake
	EpochFile string `json:"epochFile" yaml:"epochFile" mapstructure:"epochFile"`
}

var node *snowflake.Node

type epochData struct {
	Epoch int64 `json:"epoch"`
}

// Init 初始化节点，启动时调用一次；epochFile 不存在时以当天零点为基点并写入文件
func Init(nodeID int64, epochFile string) error {
	if epochFile == "" {
		epochFile = "snowflake"
	}
	var data epochData
	body, err := os.ReadFile(epochFile)
	switch {
	case err == nil:
		if err = json.Unmarshal(body, &data); err != nil {
			return errors.Wrapf(err, "snowflake: 基点文件 %s 格式不正确", epochFile)
		}
	case !os.IsNotExist(err):
		return errors.Wrapf(err, "snowflake: 读取基点文件 %s 失败", epochFile)
	}
	if data.Epoch <= 0 || data.Epoch > time.Now().UnixMilli() {
		n := time.Now()
		data.Epoch = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local).UnixMilli()
		if err = writeEpoch(epochFile, data); err != nil {
			return err
		}
	}
	snowflake.Epoch = data.Epoch

	if node, err = snowflake.NewNode(nodeID); err != nil {
		return errors.Wrapf(err, "snowflake: 创建节点失败(node=%d，范围 0~1023)", nodeID)
	}
	return nil
}

// InitConfig 按配置初始化
func InitConfig(cfg Config) error { return Init(cfg.Node, cfg.EpochFile) }

// writeEpoch 原子写入：先写临时文件再重命名
func writeEpoch(file string, data epochData) error {
	content, err := json.Marshal(data)
	if err != nil {
		return errors.Wrap(err, "snowflake: 基点序列化失败")
	}
	if dir := filepath.Dir(file); dir != "." {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return errors.Wrapf(err, "snowflake: 创建基点文件目录 %s 失败", dir)
		}
	}
	tmp := file + ".tmp"
	if err = os.WriteFile(tmp, content, 0o644); err != nil {
		return errors.Wrapf(err, "snowflake: 写入基点文件 %s 失败", tmp)
	}
	if err = os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return errors.Wrapf(err, "snowflake: 保存基点文件 %s 失败", file)
	}
	return nil
}

// GenID 生成字符串形式的雪花ID，未 Init 时 panic
func GenID() string {
	return node.Generate().String()
}

// GenInt64 生成整数形式的雪花ID(数据库主键)，未 Init 时 panic
func GenInt64() int64 {
	return node.Generate().Int64()
}
