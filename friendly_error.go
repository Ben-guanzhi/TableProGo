package main

// 数据库错误的人类可读翻译：内核 dberror 分类出稳定错误码，这里只补一句
// 用户能行动的中文提示，原始错误串永远保留在后面（日志与"复制详情"仍可用）。
// 覆盖三类最常见的可行动错误；其余原文直出，不假装翻译。

import (
	"fmt"

	"github.com/dbcore/dbcore/dberror"
)

// friendlyDBError decorates a driver error with an actionable hint when the
// class matches one of the common, user-fixable situations.
func friendlyDBError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch dberror.Classify(err) {
	case dberror.UniqueViolation:
		return fmt.Sprintf("唯一键冲突：这个值已经存在，换个值或改用已存在的行。(%s)", msg)
	case dberror.ForeignKeyViolation:
		return fmt.Sprintf("外键冲突：引用的行不存在，或还有行引用它（先处理引用再删改）。(%s)", msg)
	case dberror.Deadlock:
		return fmt.Sprintf("发生死锁：另一条事务持有冲突的锁，重试本语句通常可以解决。(%s)", msg)
	case dberror.ConnectionBroken:
		return fmt.Sprintf("连接已断开：服务端或网络中断，请重新连接后重试。(%s)", msg)
	case dberror.Timeout:
		return fmt.Sprintf("等待超时：服务端没有在时限内响应。(%s)", msg)
	}
	return msg
}
