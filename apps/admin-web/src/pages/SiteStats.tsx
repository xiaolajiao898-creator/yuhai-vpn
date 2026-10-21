import './site-stats.css';

type CountWindow = {today?: string; history?: string};
type DownloadWindow = {today?: string; week?: string; month?: string; previous_month?: string; history?: string};
type SiteSection = {
  timezone?: string;
  pageviews?: CountWindow;
  visitors?: CountWindow;
  downloads?: {android?: DownloadWindow; windows?: DownloadWindow; android_legacy?: DownloadWindow};
};

function text(value: unknown) {
  return typeof value === 'string' && /^[0-9]{1,20}$/.test(value) ? value : '—';
}

function Visit({label, hint, value}: {label: string; hint: string; value: unknown}) {
  return <article className="site-stat">
    <span>{label}</span>
    <strong className="numeric">{text(value)}</strong>
    <small>{hint}</small>
  </article>;
}

function Channel({title, row}: {title: string; row: DownloadWindow | undefined}) {
  const cells: Array<[string, unknown]> = [
    ['今日', row?.today],
    ['本周', row?.week],
    ['本月', row?.month],
    ['上月', row?.previous_month],
    ['历史', row?.history],
  ];
  return <div className="site-channel">
    <h3>{title}</h3>
    <dl>{cells.map(([label, value]) => <div key={label}><dt>{label}</dt><dd className="numeric">{text(value)}</dd></div>)}</dl>
  </div>;
}

export function SiteStats({site}: {site: SiteSection}) {
  return <section className="site-stats">
    <header>
      <div>
        <div className="eyebrow">SITE</div>
        <h2>官网访问与下载</h2>
      </div>
      <span>时区 {site.timezone || 'Asia/Kuala_Lumpur'}</span>
    </header>
    <div className="site-visits">
      <Visit label="今日浏览" hint="PV · 限流后的次数" value={site.pageviews?.today} />
      <Visit label="历史浏览" hint="PV · 累计次数" value={site.pageviews?.history} />
      <Visit label="今日访客" hint="UV · 今日不同的地址哈希" value={site.visitors?.today} />
      <Visit label="历史访客" hint="UV · 曾经出现过的地址哈希，不是每日相加" value={site.visitors?.history} />
    </div>
    <div className="site-channels">
      <Channel title="安卓安装包" row={site.downloads?.android} />
      <Channel title="Windows 安装包" row={site.downloads?.windows} />
      <Channel title="旧版安卓安装包" row={site.downloads?.android_legacy} />
    </div>
    <p>浏览：同一地址哈希 30 秒内只计 1 次，每天最多 30 次；空的或明显是机器人的访问不计入。下载：同一地址哈希、同一安装包、同一天只计 1 次，多次点击仍会下载。本周从星期一开始，本月算到今天，上月是完整的上一个自然月。不保存明文 IP。</p>
  </section>;
}
