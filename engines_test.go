package main

import "testing"

// TestBuiltinEngineAlias 归一口径: 协议兼容引擎映射到承载其线协议的内置引擎,
// 其余拼写原样返回。客户端其余分支 (driverFor / connSpecAt / poolFor) 都经它路由。
func TestBuiltinEngineAlias(t *testing.T) {
	cases := map[string]string{
		"mysql":      "mysql",
		"postgresql": "postgresql",
		"sqlite":     "sqlite",
		"sqlserver":  "sqlserver",
		"oracle":     "oracle",
		"redis":      "redis",
		"oceanbase":  "mysql",
		"greatsql":   "mysql",
		"starrocks":  "mysql",
		"doris":      "mysql",
		"opengauss":  "postgresql",
		"gaussdb":    "postgresql",
		"kingbase":   "postgresql",
		"kingbasees": "postgresql",
		" OceanBase": "mysql",
		" OpenGauss": "postgresql",
	}
	for in, want := range cases {
		if got := builtinEngine(in); got != want {
			t.Errorf("builtinEngine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEngineDefaultPort(t *testing.T) {
	cases := map[string]int{
		"mysql":      0, // 内置引擎的常规端口由前端预填, 后端不覆盖
		"postgresql": 0,
		"oceanbase":  2881,
		"greatsql":   0,
		"starrocks":  9030,
		"doris":      9030,
		"opengauss":  5432,
		"gaussdb":    5432,
		"kingbase":   5432,
	}
	for in, want := range cases {
		if got := engineDefaultPort(in); got != want {
			t.Errorf("engineDefaultPort(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestBuiltinEngineDefaultPort 别名未声明端口时回落到底层引擎的常规端口。
func TestBuiltinEngineDefaultPort(t *testing.T) {
	if got := builtinEngineDefaultPort("oceanbase"); got != 3306 {
		t.Errorf("builtinEngineDefaultPort(oceanbase) = %d, want 3306 (oceanbase 自己声明了 2881, 但该函数只回答内置引擎)", got)
	}
	if got := builtinEngineDefaultPort("opengauss"); got != 5432 {
		t.Errorf("builtinEngineDefaultPort(opengauss) = %d, want 5432", got)
	}
	if got := builtinEngineDefaultPort("mysql"); got != 3306 {
		t.Errorf("builtinEngineDefaultPort(mysql) = %d, want 3306", got)
	}
	if got := builtinEngineDefaultPort("postgresql"); got != 5432 {
		t.Errorf("builtinEngineDefaultPort(postgresql) = %d, want 5432", got)
	}
}

// TestEngineDefaultPortFor 端口回落链: 显式端口 > 别名端口 > 内置引擎端口。
func TestEngineDefaultPortFor(t *testing.T) {
	if got := engineDefaultPortFor(ConnectionConfig{Type: "mysql", Port: 3307}); got != 3307 {
		t.Errorf("显式端口应优先: %d", got)
	}
	if got := engineDefaultPortFor(ConnectionConfig{Type: "oceanbase"}); got != 2881 {
		t.Errorf("OceanBase 应回落 2881: %d", got)
	}
	if got := engineDefaultPortFor(ConnectionConfig{Type: "mysql"}); got != 3306 {
		t.Errorf("MySQL 应回落 3306: %d", got)
	}
	if got := engineDefaultPortFor(ConnectionConfig{Type: "postgresql"}); got != 5432 {
		t.Errorf("PostgreSQL 应回落 5432: %d", got)
	}
}

// TestProtocolFamilyPredicates 谓词与 hasBuiltinDriver 的联动: 别名引擎在
// hasBuiltinDriver 下可用 (走底层驱动), 谓词把它们归入正确的协议族。
func TestProtocolFamilyPredicates(t *testing.T) {
	for _, id := range []string{"mysql", "oceanbase", "greatsql", "starrocks", "doris"} {
		if !isMySQLLike(id) {
			t.Errorf("isMySQLLike(%s) 应为 true", id)
		}
	}
	for _, id := range []string{"postgresql", "opengauss", "gaussdb", "kingbase", "kingbasees"} {
		if !isPostgresLike(id) {
			t.Errorf("isPostgresLike(%s) 应为 true", id)
		}
	}
	if isMySQLLike("postgresql") || isPostgresLike("mysql") {
		t.Fatal("两族不得交叉")
	}
	for _, id := range []string{"oceanbase", "opengauss", "kingbase", "mysql", "postgresql"} {
		if !hasBuiltinDriver(id) {
			t.Errorf("hasBuiltinDriver(%s) 应为 true (别名走底层驱动)", id)
		}
	}
}
