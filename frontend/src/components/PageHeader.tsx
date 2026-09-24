import type { ReactNode } from 'react';

interface PageHeaderProps {
  title: ReactNode;
  breadcrumb?: ReactNode;
  icon?: ReactNode;
  backAction?: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  badge?: ReactNode;
  actions?: ReactNode;
  className?: string;
}

export function PageHeader({
  title,
  breadcrumb,
  icon,
  backAction,
  description,
  meta,
  badge,
  actions,
  className = '',
}: PageHeaderProps) {
  return (
    <header className={`nf-page-header ${className}`.trim()}>
      <div className="nf-page-header__identity">
        {breadcrumb && <nav className="nf-page-header__breadcrumb" aria-label="Хлебные крошки">{breadcrumb}</nav>}
        <div className="nf-page-header__title-row">
          {backAction && <div className="nf-page-header__back">{backAction}</div>}
          {icon && <span className="nf-page-header__icon" aria-hidden="true">{icon}</span>}
          <div className="nf-page-header__copy">
            <div className="nf-page-header__heading"><h1>{title}</h1>{badge}</div>
            {description && <p>{description}</p>}
          </div>
          {meta && <div className="nf-page-header__meta">{meta}</div>}
        </div>
      </div>
      {actions && <div className="nf-page-tools">{actions}</div>}
    </header>
  );
}
