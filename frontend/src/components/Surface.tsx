import type { HTMLAttributes, ReactNode } from 'react';

interface SurfaceProps extends Omit<HTMLAttributes<HTMLElement>, 'title'> {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  as?: 'section' | 'article' | 'div';
}

export function Surface({ title, description, actions, children, className = '', as: Element = 'section', ...props }: SurfaceProps) {
  return (
    <Element className={`nf-surface ${className}`} {...props}>
      {(title || description || actions) && (
        <header className="nf-surface__header">
          <div>
            {title && <h2>{title}</h2>}
            {description && <p>{description}</p>}
          </div>
          {actions && <div className="nf-surface__actions">{actions}</div>}
        </header>
      )}
      {children}
    </Element>
  );
}
