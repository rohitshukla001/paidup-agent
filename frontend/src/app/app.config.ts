import { ApplicationConfig, provideBrowserGlobalErrorListeners } from '@angular/core';
import { provideRouter } from '@angular/router';
import { environment } from '../environments/environment';
import { Backend } from './api/backend';
import { HttpBackend } from './api/http-backend';
import { MockBackend } from './api/mock-backend';
import { routes } from './app.routes';

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    provideRouter(routes),
    { provide: Backend, useClass: environment.mock ? MockBackend : HttpBackend },
  ],
};
