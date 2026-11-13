import {useEffect, useState} from 'react';
import {Alert, Button, Table, Tag} from 'antd';
import {adminApi} from '../api/client';

type Release = {
  id: number;
  platform: string;
  version: string;
  build: number;
  url: string;
  sha256: string;
  size: number;
  released_at: string;
  min_supported_build: number;
  notes: string;
  current: boolean;
};

export function AppReleases() {
  const [rows, setRows] = useState<Release[]>([]);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  async function load() {
    setBusy(true);
    setError('');
    try {
      const body = await adminApi.get<{data: Release[]}>('/app/releases');
      setRows(body.data ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : '暂时无法读取安装包');
    } finally {
      setBusy(false);
    }
  }
  useEffect(() => { void load(); }, []);
  return <section className="page-intro">
    <div>
      <div className="eyebrow">CURRENT DOWNLOAD</div>
      <h2>当前安装包</h2>
      <p>官网的「最新下载」指向这里标成当前的那一份。切回以前的包不会改价格，也不会改数据库里的其他表。</p>
      {error && <Alert type="error" showIcon message={error}/>}
      <Table
        rowKey="id"
        loading={busy}
        dataSource={rows}
        pagination={false}
        columns={[
          {title: '平台', dataIndex: 'platform'},
          {title: '版本', dataIndex: 'version'},
          {title: '构建', dataIndex: 'build'},
          {title: '当前', dataIndex: 'current', render: (value: boolean) => value ? <Tag color="green">当前</Tag> : '—'},
          {title: '', render: (_, row) => <Button disabled={row.current || busy} onClick={() => { void adminApi.post(`/app/releases/${row.id}/current`, {}).then(load); }}>切回这一份</Button>},
        ]}
      />
    </div>
  </section>;
}
