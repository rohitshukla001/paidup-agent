import { inject } from '@angular/core';
import { CanActivateFn, Router, Routes } from '@angular/router';
import { Backend } from './api/backend';
import { Home } from './home';
import { Login } from './login/login';

// signedIn sends a visitor without a session to the login page.
const signedIn: CanActivateFn = async () => {
  const router = inject(Router);
  try {
    return (await inject(Backend).session()) || router.parseUrl('/login');
  } catch {
    return router.parseUrl('/login');
  }
};

export const routes: Routes = [
  { path: 'login', component: Login, title: 'Log in | PaidUp' },
  { path: '', component: Home, canActivate: [signedIn], title: 'PaidUp' },
  { path: '**', redirectTo: '' },
];
