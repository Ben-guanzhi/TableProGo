// 让「可点击的任意元素」也能用键盘触发。
//
// 为什么需要：`<div onClick={...}>` 只能用鼠标——Tab 到不了它，Enter/Space
// 也没反应。加上 role="button" + tabIndex={0} + onKeyDown 才是一个真正的
// 可访问控件。这个辅助把键盘那半封装起来，调用处只需三行。
//
// 用法：
//   <div role="button" tabIndex={0}
//        onClick={open} onKeyDown={onEnter(open)}>

import type React from 'react';

export function onEnter(fn: () => void): React.KeyboardEventHandler {
  return (e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      fn();
    }
  };
}
