import React from 'react';
import ReactDOM from 'react-dom/client';
import { ConfigProvider, theme } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from './App';

const qc = new QueryClient();

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ConfigProvider locale={zhCN} button={{autoInsertSpace:false}} theme={{ algorithm: theme.defaultAlgorithm, token: { colorPrimary: '#157fa6', colorText: '#112c43', colorBorder: '#dce5ed', borderRadius: 10, fontFamily: "-apple-system, BlinkMacSystemFont, 'PingFang SC', 'Noto Sans SC', 'Segoe UI', sans-serif" } }}>
      <QueryClientProvider client={qc}>
        <App />
      </QueryClientProvider>
    </ConfigProvider>
  </React.StrictMode>,
);
