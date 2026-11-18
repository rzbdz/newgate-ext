package breaker

import (
	"reflect"
	"strings"
	"testing"
)

// 这两条锁的是**端口的形状**，不是行为：健康表借给外面的能力，与它自己内部怎么
// 接线，是两件事；把它们混在一个接口上，就是「谁都能悄悄改」的开始。
//
// 起因是一次真实的 review（2026-09-21）。`Breaker` 上曾经挂着三个 setter——
// `SetPolicy` / `SetErrorHandler` / `SetVerifier`——而全仓库**没有任何一处**透过
// 这个接口调它们（调用点全在包内，作用于具体的 `*table`）。多出来的部分不是中性的：
// `SetVerifier` 是上闸前那道主动诊断的开关，一句 `SetVerifier(nil)` 就能把它卸掉，
// 不报错、依赖图上没有边、review 也看不出有人碰过它。而 `SetPolicy` **零调用者**，
// 是为一个还没做的功能预留的 API。
//
// 判据是「一个能力只该有一扇门，门开在拥有它的模块上」：数据面借给策略的运行期
// 能力走 `policy.EnvBinder.BindEnv`（那条缝有主、有向、时机明确），而不是从端口上
// 开一个谁都能推的写门。

// TestThePortCarriesNoUnownedSetters 断言公开端口上一个 setter 都没有。
//
// 按名字前缀判，而不是拿一张白名单比对：白名单每加一个正当的方法就要改一次，
// 改的次数多了就变成「看见这条红就顺手加一行」——那时它已经不是棘轮了。前缀是
// 命名约定、不是证明，所以真正的判据写在上面那段注释里；这条测试守的是**形状**
// （一个能被外面改的动词），而形状恰恰是这个反模式唯一稳定的特征。
func TestThePortCarriesNoUnownedSetters(t *testing.T) {
	typ := reflect.TypeOf((*Breaker)(nil)).Elem()
	names := make([]string, 0, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		names = append(names, typ.Method(i).Name)
	}
	for _, n := range names {
		if strings.HasPrefix(n, "Set") {
			t.Errorf("公开端口上有 %s：它是一扇谁都能推的写门。\n"+
				"往里接线请走 BindEnv（policy.EnvBinder），或者把方法留在 *table 上——\n"+
				"理由见 api.go 那段「这个接口为什么是读多写少」。", n)
		}
	}
	// 一条会让上面那个循环空转成绿的空表守卫：端口不可能一个方法都没有。
	if len(names) == 0 {
		t.Fatal("反射没读到任何方法——上面那个循环是空转的")
	}
}

// TestTheWiringMethodsStillExistOnTheTable 是上一条的另一半。
//
// 砍端口**不是**砍能力：那三个方法照旧在，`BindEnv` 靠它们把日志出口与上闸前
// 诊断接回健康表。只有一条测试盯着「端口上没有 setter」，下一次重构就可能顺手
// 把实现也删掉——而删掉之后编译照样过（没人透过端口调它们），症状是上闸前诊断
// **静默**不再运行：坏 binding 照摘，只是那道「先再要一次主动证据」没有了。
func TestTheWiringMethodsStillExistOnTheTable(t *testing.T) {
	concrete := reflect.TypeOf(&table{})
	for _, name := range []string{"SetPolicy", "SetErrorHandler", "SetVerifier"} {
		if _, ok := concrete.MethodByName(name); !ok {
			t.Errorf("*table 上没有 %s 了——端口的收窄不该把接线方法一起砍掉，"+
				"BindEnv 还要用它（见 api.go）", name)
		}
	}
}
