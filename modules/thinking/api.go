package thinking

import (
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	modules "github.com/rzbdz/newgate/component"
)

// Service 提供与模型无关的 thinking 降级操作。
// 具体模型插件依赖这个端口，而不是反向依赖 thinking 的内部实现。
type Service interface {
	BestEffortDisable(body []byte, request *special.Request) ([]byte, []string, error)
}

// Capability 标识唯一的 thinking 策略服务。
var Capability = modules.NewCapability[Service]("thinking")
