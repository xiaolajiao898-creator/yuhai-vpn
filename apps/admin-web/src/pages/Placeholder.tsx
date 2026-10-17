import { Empty, Typography } from 'antd';

export function Placeholder({ title }: { title: string }) {
  return (
    <>
      <Typography.Title level={4}>{title}</Typography.Title>
      <Empty description="此模块页面尚未接入" />
    </>
  );
}
