import { IconArrowLeft } from '@tabler/icons-react';
import { Link } from 'react-router-dom';
import { Surface } from '../components/Surface';

export function PlaceholderPage({ title }: { title: string }) {
  return (
    <main className="nf-page nf-placeholder-page">
      <header className="nf-page-header"><h1>{title}</h1></header>
      <Surface>
        <div className="nf-state-view">
          <span>Этот экран будет перенесён в новый design system следующим этапом.</span>
          <Link to="/nodes" className="nf-text-link"><IconArrowLeft size={16} /> К нодам</Link>
        </div>
      </Surface>
    </main>
  );
}
