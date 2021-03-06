import { NgModule } from '@angular/core';
import { Routes, RouterModule } from '@angular/router';
import { AuthGuardService } from '../auth/shared/auth-guard.service';
import { BuildComponent } from './build/build.component';
import { JobComponent } from './job/job.component';
import { IndexComponent } from './index/index.component';

const routes: Routes = [
  { path: 'builds', component: IndexComponent, canActivate: [AuthGuardService] },
  { path: 'builds/:id', component: BuildComponent, canActivate: [AuthGuardService] },
  { path: 'builds/:buildid/:jobid', component: JobComponent, canActivate: [AuthGuardService] }
];

@NgModule({
  imports: [RouterModule.forChild(routes)],
  exports: [RouterModule]
})
export class BuildsRoutingModule {}
