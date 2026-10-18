import {useCallback, useEffect, useState} from 'react';
import {Alert, Button, Input, InputNumber, Modal, Select, Space, Table, Tag, Typography} from 'antd';
import type {AdminSession} from '../api/client';
import {adminApi} from '../api/client';
import {prepareAction} from '../api/operations';

type CardType = {
  code: string;
  name: string;
  duration_days: number;
  tier: number;
  device_limit: number;
  unlimited_traffic: boolean;
};
type CodeRow = {
  id: string;
  hint: string;
  batch_id: string;
  card_type: string;
  card_name: string;
  duration_days: number;
  tier: number;
  device_limit: number;
  unlimited_traffic: boolean;
  note: string;
  status: 'unused' | 'used' | 'revoked';
  created_at: string;
  redeemed_account_no: string | null;
  redeemed_at: string | null;
};
type CodePage = {items: CodeRow[]; total: string; page: number; page_size: number};
type BatchResult = {
  batch_id: string;
  card_type: string;
  card_name: string;
  quantity: number;
  duration_days: number;
  note: string;
  codes: string[];
};

const statusLabel: Record<CodeRow['status'], string> = {unused: '未使用', used: '已使用', revoked: '已作废'};

function errorText(e: unknown) {
  const status = (e as {status?: number})?.status;
  if (status === 401) return '登录已失效，请重新登录';
  if (status === 403) return '当前账号没有兑换码权限';
  return e instanceof Error ? e.message : '操作失败，请重试';
}

function csvFor(batch: BatchResult) {
  const lines = ['code,card_type,card_name,days,note,batch_id'];
  for (const code of batch.codes) {
    lines.push([code, batch.card_type, batch.card_name, String(batch.duration_days), batch.note, batch.batch_id].map(cell => `"${String(cell).replaceAll('"', '""')}"`).join(','));
  }
  return `\uFEFF${lines.join('\n')}`;
}

function download(name: string, text: string) {
  const blob = new Blob([text], {type: 'text/csv;charset=utf-8'});
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

export function RedeemCodesPage({session, demo = false}: {session: AdminSession; demo?: boolean}) {
  const canRead = session.permissions.includes('redeem_codes.read');
  const canGenerate = session.permissions.includes('redeem_codes.generate');
  const canExport = session.permissions.includes('redeem_codes.export');
  const canRevoke = session.permissions.includes('redeem_codes.revoke');
  const [types, setTypes] = useState<CardType[]>([]);
  const [page, setPage] = useState<CodePage>();
  const [filters, setFilters] = useState({card_type: '', status: '', batch_id: '', note: ''});
  const [applied, setApplied] = useState(filters);
  const [listPage, setListPage] = useState(1);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [draft, setDraft] = useState<{card: CardType; quantity: number; note: string} | null>(null);
  const [totp, setTotp] = useState('');
  const [busy, setBusy] = useState(false);
  const [fresh, setFresh] = useState<BatchResult | null>(null);
  const [reload, setReload] = useState(0);

  const load = useCallback(() => {
    if (!canRead || demo) return;
    const ctrl = new AbortController();
    setLoading(true);
    const query = new URLSearchParams({page: String(listPage), page_size: '20'});
    for (const [key, value] of Object.entries(applied)) if (value) query.set(key, value);
    adminApi.get<CodePage>(`/redeem-codes?${query}`, ctrl.signal)
      .then(setPage)
      .catch(e => { if (!ctrl.signal.aborted) setError(errorText(e)); })
      .finally(() => { if (!ctrl.signal.aborted) setLoading(false); });
    return () => ctrl.abort();
  }, [applied, canRead, demo, listPage]);

  useEffect(() => load(), [load, reload]);
  useEffect(() => {
    if (!canRead || demo) return;
    adminApi.get<{items: CardType[]}>('/redeem-card-types').then(r => setTypes(r.items)).catch(e => setError(errorText(e)));
  }, [canRead, demo]);

  async function generate() {
    if (!draft) return;
    setBusy(true);
    setError('');
    try {
      const prepared = await prepareAction({
        title: `生成${draft.card.name}`,
        method: 'POST',
        path: '/redeem-codes/batches',
        permission: 'redeem_codes.generate',
        body: {operation_id: crypto.randomUUID(), card_type: draft.card.code, quantity: draft.quantity, note: draft.note.trim()},
      }, session);
      const result = await prepared.confirm(totp) as BatchResult;
      if (!result || !Array.isArray(result.codes) || result.codes.length !== draft.quantity) throw new Error('生成回执不完整，请先在列表里核对，不要立刻再生成一批');
      setFresh(result);
      setDraft(null);
      setTotp('');
      setReload(n => n + 1);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  async function revoke(row: CodeRow) {
    const reason = window.prompt('作废原因', '手动作废')?.trim();
    if (!reason) return;
    const code = window.prompt('动态验证码（6 位）');
    if (!code) return;
    setBusy(true);
    setError('');
    try {
      await (await prepareAction({
        title: '作废兑换码',
        method: 'POST',
        path: `/redeem-codes/${row.id}/revoke`,
        permission: 'redeem_codes.revoke',
        body: {operation_id: crypto.randomUUID(), reason},
      }, session)).confirm(code);
      setReload(n => n + 1);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  async function exportBatch(batchId: string) {
    const reason = window.prompt('导出原因', '补发明文')?.trim();
    if (!reason) return;
    const code = window.prompt('动态验证码（6 位）');
    if (!code) return;
    setBusy(true);
    setError('');
    try {
      const result = await (await prepareAction({
        title: '导出兑换码',
        method: 'POST',
        path: `/redeem-codes/batches/${batchId}/export`,
        permission: 'redeem_codes.export',
        body: {operation_id: crypto.randomUUID(), reason},
      }, session)).confirm(code) as {batch_id: string; card_type: string; card_name: string; note: string; codes: {code: string; status: string}[]};
      const codes = result.codes.filter(item => item.status === 'unused').map(item => item.code);
      const batch: BatchResult = {batch_id: result.batch_id, card_type: result.card_type, card_name: result.card_name, quantity: codes.length, duration_days: 0, note: result.note, codes};
      setFresh({...batch, codes: result.codes.map(item => `${item.code}${item.status === 'unused' ? '' : '  (' + (item.status === 'used' ? '已使用' : '已作废') + ')'}`)});
      download(`redeem-${result.card_type}-${result.batch_id}.csv`, csvFor({...batch, codes: result.codes.map(item => item.code)}));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  if (demo) return <Alert type="info" showIcon message="演示模式不生成真实兑换码" />;
  if (!canRead) return <Alert type="warning" showIcon message="当前账号没有查看兑换码的权限" />;

  return <div>
    <Typography.Title level={3} style={{marginTop: 0}}>兑换码</Typography.Title>
    <Typography.Paragraph type="secondary">生成后把码交给对方，对方在 App「兑换码」里输入。周卡 7 天、月卡 30 天、年卡 365 天，都是 L1、3 台设备、不限流量。已有套餐会从当前到期日往后加，不会把更高等级或更长的剩余时间改短。</Typography.Paragraph>
    {error && <Alert style={{marginBottom: 16}} type="error" showIcon message={error} closable onClose={() => setError('')} />}
    <Space wrap style={{marginBottom: 16}}>
      {types.map(card => <Button key={card.code} type="primary" disabled={!canGenerate} onClick={() => { setTotp(''); setDraft({card, quantity: 10, note: ''}); }}>生成{card.name}</Button>)}
      {types.length === 0 && <Typography.Text type="secondary">正在读取卡类型…</Typography.Text>}
    </Space>
    <Space wrap style={{marginBottom: 16}}>
      <Select allowClear placeholder="卡类型" style={{width: 120}} value={filters.card_type || undefined} onChange={v => setFilters(f => ({...f, card_type: v ?? ''}))} options={types.map(t => ({value: t.code, label: t.name}))} />
      <Select allowClear placeholder="状态" style={{width: 120}} value={filters.status || undefined} onChange={v => setFilters(f => ({...f, status: v ?? ''}))} options={[{value: 'unused', label: '未使用'}, {value: 'used', label: '已使用'}, {value: 'revoked', label: '已作废'}]} />
      <Input placeholder="批次编号" style={{width: 140}} value={filters.batch_id} onChange={e => setFilters(f => ({...f, batch_id: e.target.value.trim()}))} />
      <Input placeholder="备注" style={{width: 180}} value={filters.note} onChange={e => setFilters(f => ({...f, note: e.target.value}))} />
      <Button onClick={() => { setListPage(1); setApplied(filters); }}>筛选</Button>
    </Space>
    <Table rowKey="id" loading={loading} dataSource={page?.items ?? []} pagination={{current: listPage, pageSize: 20, total: Number(page?.total ?? 0), onChange: setListPage, showSizeChanger: false}} columns={[
      {title: '掩码', dataIndex: 'hint'},
      {title: '类型', dataIndex: 'card_name'},
      {title: '天数', dataIndex: 'duration_days'},
      {title: '批次', dataIndex: 'batch_id'},
      {title: '备注', dataIndex: 'note', render: (v: string) => v || '—'},
      {title: '状态', dataIndex: 'status', render: (v: CodeRow['status']) => <Tag color={v === 'unused' ? 'blue' : v === 'used' ? 'green' : 'default'}>{statusLabel[v]}</Tag>},
      {title: '兑换账号', dataIndex: 'redeemed_account_no', render: (v: string | null) => v || '—'},
      {title: '兑换时间', dataIndex: 'redeemed_at', render: (v: string | null) => v ? new Date(v).toLocaleString() : '—'},
      {title: '操作', render: (_: unknown, row: CodeRow) => <Space>
        {canRevoke && row.status === 'unused' && <Button size="small" disabled={busy} onClick={() => void revoke(row)}>作废</Button>}
        {canExport && <Button size="small" disabled={busy} onClick={() => void exportBatch(row.batch_id)}>导出该批</Button>}
      </Space>},
    ]} />
    <Modal title={draft ? `生成${draft.card.name}` : ''} open={!!draft} okText="生成" confirmLoading={busy} onCancel={() => setDraft(null)} onOk={() => void generate()} okButtonProps={{disabled: !draft || draft.quantity < 1}}>
      {draft && <>
        <Typography.Paragraph>{draft.card.duration_days} 天 · L{draft.card.tier} · {draft.card.device_limit} 台设备 · {draft.card.unlimited_traffic ? '不限流量' : '按卡面流量'}</Typography.Paragraph>
        <div style={{marginBottom: 12}}>数量 <InputNumber min={1} max={500} value={draft.quantity} onChange={v => setDraft(d => d && {...d, quantity: Number(v) || 1})} /></div>
        <Input style={{marginBottom: 12}} placeholder="备注（可选，例如发给谁）" maxLength={200} value={draft.note} onChange={e => setDraft(d => d && {...d, note: e.target.value})} />
        <Input placeholder="6 位动态验证码" maxLength={6} value={totp} onChange={e => setTotp(e.target.value.replace(/\D/g, ''))} />
      </>}
    </Modal>
    <Modal title={fresh ? `${fresh.card_name}已生成` : ''} open={!!fresh} footer={null} onCancel={() => setFresh(null)} width={640}>
      {fresh && <>
        <Alert type="warning" showIcon style={{marginBottom: 12}} message="请马上复制或下载。关闭这个窗口后，列表里只看得到后四位。需要完整码时，用「导出该批」再验证一次。" />
        <Input.TextArea readOnly rows={8} value={fresh.codes.join('\n')} />
        <Space style={{marginTop: 12}}>
          <Button onClick={() => void navigator.clipboard.writeText(fresh.codes.join('\n'))}>复制全部</Button>
          <Button onClick={() => download(`redeem-${fresh.card_type}-${fresh.batch_id}.csv`, csvFor(fresh))}>下载 CSV</Button>
        </Space>
      </>}
    </Modal>
  </div>;
}
