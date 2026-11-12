// 隐私模式（录屏/共享屏幕场景）的纯规则。
//
// 为什么是"过滤层"而不是"不渲染"：用户仍在工作，只是不希望旁人或录制看到
// 主机名、库名和行内容。所以元素照常布局（布局不跳动，截图对比可判断），
// 只是内容被遮罩，悬停/聚焦时才临时显形——录制软件拿不到悬停。
//
// 两种遮罩刻意分开：
//   identity —— 连接身份（主机、端口、库、用户、连接名）。全遮，只留引擎类型：
//                "这是 MySQL" 不敏感，"这是哪个库"才是。
//   cells    —— 结果集单元格。等宽遮罩，保留列宽与行高，便于继续对齐阅读。
//
// 这里只放判定与文案，DOM 动作在组件里，CSS 在 index.css 的 .privacy 规则里。

export interface PrivacyTarget {
  /** 连接名称（用户常常直接写成 prod-db.example.com）。 */
  name?: string;
  /** 主机名或 IP。 */
  host?: string;
  /** 端口。 */
  port?: number | string;
  /** 库名 / schema。 */
  database?: string;
  /** 用户名。 */
  user?: string;
  /** 引擎类型：不遮。 */
  engine?: string;
}

/** 遮罩字符：一个可见的点，UI 里统一用这个。 */
export const MASK = '••••••';

/**
 * maskText 把任意文本换成固定长度的遮罩。
 *
 * 刻意不留长度特征：`db1` 和 `db-production-2024` 遮完一样长，否则"长度"本身
 * 就是信息（一次截屏就能看出你有几个生产库、名字多长）。
 */
export const maskText = (value: string | number | null | undefined): string => {
  if (value === null || value === undefined) return '';
  const text = String(value).trim();
  return text === '' ? '' : MASK;
};

/**
 * hostPort 组合 host:port 并按隐私开关遮罩。
 *
 * 组合在遮罩之前完成：分开遮会留下 "••••••:3306" 里真实的端口，而端口常常就
 * 是哪个环境的线索（3306 测试机 / 15432 生产）。
 */
export const hostPort = (
  host: string | undefined,
  port: number | string | undefined,
  privacy: boolean,
): string => {
  const hostText = (host ?? '').trim();
  const portText = port === undefined || port === null ? '' : String(port).trim();
  if (privacy) {
    return hostText || portText ? MASK : '';
  }
  if (!hostText && !portText) return '';
  if (!portText) return hostText;
  if (!hostText) return portText;
  return `${hostText}:${portText}`;
};

/**
 * identityLabel 是连接在侧栏/标题里的一行文字。
 *
 * 隐私模式下只保留引擎名，因为"这是一个 MySQL 连接"不影响判断，而连到哪个库
 * 才是要遮的东西。
 */
export const identityLabel = (target: PrivacyTarget, privacy: boolean): string => {
  const engine = (target.engine ?? '').trim();
  if (!privacy) {
    const name = (target.name ?? '').trim();
    if (name) return name;
    const composed = [hostPort(target.host, target.port, false), (target.database ?? '').trim()]
      .filter(Boolean)
      .join(' · ');
    return composed || engine;
  }
  return engine ? `${MASK} (${engine})` : MASK;
};

/**
 * detailLine 是身份详情（用户 · 主机 · 库），连接对话框与提示里用。
 */
export const detailLine = (target: PrivacyTarget, privacy: boolean): string => {
  const parts: string[] = [];
  if (target.user?.trim()) parts.push(target.user.trim());
  const hp = hostPort(target.host, target.port, privacy);
  if (hp) parts.push(hp);
  if (target.database?.trim()) parts.push(target.database.trim());
  if (parts.length === 0) return '';
  if (!privacy) return parts.join(' · ');
  // 隐私模式：所有身份项合成一个遮罩，不逐项遮——逐项会泄露"有几项"。
  return MASK;
};

/**
 * fileStem 用于导出文件名：隐私模式下不把库名/表名写进文件名。
 *
 * 这一条容易被忽略：即使界面遮住了，导出的 `shop-prod-orders.xlsx` 仍然把库名
 * 留在了磁盘和聊天记录里。
 */
export const fileStem = (parts: Array<string | undefined>, privacy: boolean): string => {
  const cleaned = parts.map(p => (p ?? '').trim()).filter(Boolean);
  if (privacy) return 'export';
  const stem = cleaned.join('-').replace(/[\\/:*?"<>|]+/g, '_');
  return stem || 'export';
};

/**
 * cellMask 为结果集单元格生成占位文本。
 *
 * 定长而不是按原值长度：否则列宽会泄露值的长度（密码、卡号尤其敏感）。
 */
export const cellMask = (): string => MASK;

/**
 * shouldBlur 判断某个元素在当前状态下是否该遮罩。
 *
 * revealed 由"悬停/聚焦"提供：遮罩是防旁观与录制，不是防本人。
 */
export const shouldBlur = (privacy: boolean, revealed: boolean): boolean =>
  privacy && !revealed;

/**
 * privateTitle 决定一个原生 tooltip 该不该出现。
 *
 * 隐私模式下 tooltip 必须整个去掉，而不是换成遮罩：tooltip 的内容是浏览器渲染的
 * 原生浮层，CSS 的 .privacy 规则碰不到它——鼠标扫过去，完整的库名就出现在录屏里。
 * 这是"界面已经模糊、但悬停即泄露"的典型缺口。
 */
export const privateTitle = (text: string | undefined, privacy: boolean): string | undefined => {
  if (!text) return undefined;
  return privacy ? undefined : text;
};